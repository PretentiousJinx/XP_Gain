import 'dart:async';

import 'package:firebase_auth/firebase_auth.dart';

import '../api/token_provider.dart';

/// Supplies the Firebase ID token to [ApiClient].
///
/// Deliberately thin: it is the one class that touches the identity SDK, so the
/// rest of the app -- and every test -- works against the [TokenProvider]
/// interface instead.
class FirebaseTokenProvider implements TokenProvider {
  FirebaseTokenProvider([FirebaseAuth? auth])
      : _auth = auth ?? FirebaseAuth.instance;

  final FirebaseAuth _auth;

  User? get currentUser => _auth.currentUser;
  bool get hasSession => _auth.currentUser != null;

  @override
  Future<String?> idToken({bool forceRefresh = false}) async {
    final user = _auth.currentUser;
    if (user == null) return null;
    try {
      return await user.getIdToken(forceRefresh);
    } on FirebaseAuthException {
      // A token we cannot mint is indistinguishable from being signed out, and
      // the caller already handles that.
      return null;
    }
  }

  @override
  Future<void> signOut() => _auth.signOut();
}

/// Why a sign-in attempt stopped.
enum AuthOutcome {
  signedIn,

  /// Correct password, but the account has a second factor enrolled and the
  /// SMS code has not been supplied yet.
  needsSmsCode,

  /// Signed in, but the account has no second factor and the server requires
  /// one, so enrolment must happen before the API will serve this user.
  needsEnrollment,

  failed,
}

/// The result of a step, plus whatever the next step needs.
class AuthStep {
  const AuthStep(this.outcome, {this.message, this.resolver, this.verificationId});

  final AuthOutcome outcome;
  final String? message;

  /// Present when [outcome] is [AuthOutcome.needsSmsCode]: Firebase's handle
  /// for the half-finished sign-in.
  final MultiFactorResolver? resolver;

  /// Present once an SMS has been sent.
  final String? verificationId;

  bool get isSignedIn => outcome == AuthOutcome.signedIn;
}

/// The enrolment half of multi-factor, as an interface.
///
/// The screen depends on this rather than on [AuthService] so it can be driven
/// in tests without a configured Firebase project.
abstract class MfaEnroller {
  Future<AuthStep> startEnrollment(String phoneNumber);
  Future<AuthStep> confirmEnrollment({
    required String verificationId,
    required String smsCode,
    String displayName,
  });
}

/// Email/password sign-in with SMS multi-factor.
///
/// SMS is the only factor the server can verify: resolving it mints an ID token
/// carrying `firebase.sign_in_second_factor`, which the Go verifier checks. The
/// biometric gate is a separate, local concern and never reaches this class.
class AuthService implements MfaEnroller {
  AuthService([FirebaseAuth? auth]) : _auth = auth ?? FirebaseAuth.instance;

  final FirebaseAuth _auth;

  FirebaseAuth get raw => _auth;

  Future<AuthStep> signIn({required String email, required String password}) async {
    try {
      await _auth.signInWithEmailAndPassword(email: email, password: password);
      return _afterPrimaryFactor();
    } on FirebaseAuthMultiFactorException catch (e) {
      // Expected on any enrolled account: the password was right, and Firebase
      // is waiting for the second factor.
      return AuthStep(AuthOutcome.needsSmsCode, resolver: e.resolver);
    } on FirebaseAuthException catch (e) {
      return AuthStep(AuthOutcome.failed, message: _describe(e));
    }
  }

  Future<AuthStep> register({required String email, required String password}) async {
    try {
      await _auth.createUserWithEmailAndPassword(email: email, password: password);
      // A brand-new account cannot have a second factor yet.
      return const AuthStep(AuthOutcome.needsEnrollment);
    } on FirebaseAuthException catch (e) {
      return AuthStep(AuthOutcome.failed, message: _describe(e));
    }
  }

  AuthStep _afterPrimaryFactor() {
    final user = _auth.currentUser;
    if (user == null) return const AuthStep(AuthOutcome.failed, message: 'Sign-in did not complete.');
    return const AuthStep(AuthOutcome.signedIn);
  }

  /// Sends the SMS for a sign-in that is waiting on its second factor.
  Future<AuthStep> sendSignInCode(MultiFactorResolver resolver) async {
    final hint = resolver.hints.whereType<PhoneMultiFactorInfo>().firstOrNull;
    if (hint == null) {
      return const AuthStep(AuthOutcome.failed,
          message: 'This account has no phone factor enrolled.');
    }

    final completer = Completer<AuthStep>();
    await _auth.verifyPhoneNumber(
      multiFactorSession: resolver.session,
      multiFactorInfo: hint,
      verificationCompleted: (_) {},
      verificationFailed: (e) {
        if (!completer.isCompleted) {
          completer.complete(AuthStep(AuthOutcome.failed, message: _describe(e)));
        }
      },
      codeSent: (verificationId, _) {
        if (!completer.isCompleted) {
          completer.complete(AuthStep(AuthOutcome.needsSmsCode,
              resolver: resolver, verificationId: verificationId));
        }
      },
      codeAutoRetrievalTimeout: (_) {},
    );
    return completer.future;
  }

  /// Completes sign-in with the code the user typed.
  Future<AuthStep> submitSignInCode({
    required MultiFactorResolver resolver,
    required String verificationId,
    required String smsCode,
  }) async {
    try {
      final assertion = PhoneMultiFactorGenerator.getAssertion(
        PhoneAuthProvider.credential(
            verificationId: verificationId, smsCode: smsCode),
      );
      await resolver.resolveSignIn(assertion);
      return const AuthStep(AuthOutcome.signedIn);
    } on FirebaseAuthException catch (e) {
      return AuthStep(AuthOutcome.failed, message: _describe(e));
    }
  }

  /// Starts enrolment for a signed-in user with no second factor.
  @override
  Future<AuthStep> startEnrollment(String phoneNumber) async {
    final user = _auth.currentUser;
    if (user == null) {
      return const AuthStep(AuthOutcome.failed, message: 'Sign in first.');
    }

    final session = await user.multiFactor.getSession();
    final completer = Completer<AuthStep>();
    await _auth.verifyPhoneNumber(
      phoneNumber: phoneNumber,
      multiFactorSession: session,
      verificationCompleted: (_) {},
      verificationFailed: (e) {
        if (!completer.isCompleted) {
          completer.complete(AuthStep(AuthOutcome.failed, message: _describe(e)));
        }
      },
      codeSent: (verificationId, _) {
        if (!completer.isCompleted) {
          completer.complete(AuthStep(AuthOutcome.needsSmsCode,
              verificationId: verificationId));
        }
      },
      codeAutoRetrievalTimeout: (_) {},
    );
    return completer.future;
  }

  /// Finishes enrolment.
  ///
  /// The ID token minted *before* this call has no second-factor claim, so the
  /// caller must force a refresh afterwards or the server will keep answering
  /// 403 with a now-stale credential.
  @override
  Future<AuthStep> confirmEnrollment({
    required String verificationId,
    required String smsCode,
    String displayName = 'Phone',
  }) async {
    final user = _auth.currentUser;
    if (user == null) {
      return const AuthStep(AuthOutcome.failed, message: 'Sign in first.');
    }
    try {
      final assertion = PhoneMultiFactorGenerator.getAssertion(
        PhoneAuthProvider.credential(
            verificationId: verificationId, smsCode: smsCode),
      );
      await user.multiFactor.enroll(assertion, displayName: displayName);
      await user.getIdToken(true); // pick up sign_in_second_factor
      return const AuthStep(AuthOutcome.signedIn);
    } on FirebaseAuthException catch (e) {
      return AuthStep(AuthOutcome.failed, message: _describe(e));
    }
  }

  Future<bool> hasEnrolledFactor() async {
    final user = _auth.currentUser;
    if (user == null) return false;
    final factors = await user.multiFactor.getEnrolledFactors();
    return factors.isNotEmpty;
  }

  Future<void> sendPasswordReset(String email) =>
      _auth.sendPasswordResetEmail(email: email);

  Future<void> signOut() => _auth.signOut();

  /// Turns Firebase's codes into something a person can act on.
  ///
  /// The wrong-password and unknown-email cases are deliberately given the same
  /// wording: distinguishing them tells an attacker which addresses have
  /// accounts.
  static String _describe(FirebaseAuthException e) {
    switch (e.code) {
      case 'invalid-email':
        return 'That email address is not valid.';
      case 'user-disabled':
        return 'This account has been disabled.';
      case 'user-not-found':
      case 'wrong-password':
      case 'invalid-credential':
        return 'Incorrect email or password.';
      case 'email-already-in-use':
        return 'An account already exists for that email.';
      case 'weak-password':
        return 'Choose a longer password.';
      case 'too-many-requests':
        return 'Too many attempts. Try again shortly.';
      case 'invalid-verification-code':
        return 'That code is not correct.';
      case 'invalid-phone-number':
        return 'That phone number is not valid. Include the country code.';
      case 'network-request-failed':
        return 'Could not reach the network.';
      default:
        return e.message ?? 'Authentication failed.';
    }
  }
}

extension _FirstOrNull<T> on Iterable<T> {
  T? get firstOrNull => isEmpty ? null : first;
}
