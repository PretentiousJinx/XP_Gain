import 'dart:convert';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:local_auth/local_auth.dart';
import 'package:xp_gain/src/api/api_client.dart';
import 'package:xp_gain/src/api/token_provider.dart';
import 'package:xp_gain/src/app.dart';
import 'package:xp_gain/src/auth/auth_service.dart';
import 'package:xp_gain/src/auth/biometric_gate.dart';
import 'package:xp_gain/src/render/sprite_sheet.dart';
import 'package:xp_gain/src/screens/sign_in_screen.dart';
import 'package:xp_gain/src/state/app_controller.dart';

http.Response _json(Object body, {int status = 200}) =>
    http.Response(jsonEncode(body), status,
        headers: {'content-type': 'application/json'});

Map<String, dynamic> get _profileJson => {
      'user_id': 'u1',
      'timezone': 'UTC',
      'goals': {
        'goal_kcal': 2200,
        'goal_protein_g': 160,
        'goal_carbs_g': 220,
        'goal_fat_g': 70
      },
      'local_date': '2026-09-06',
      'day_totals': {'kcal': 0, 'protein_g': 0, 'carbs_g': 0, 'fat_g': 0},
      'remaining': {'kcal': 2200, 'protein_g': 160, 'carbs_g': 220, 'fat_g': 70},
      'character': {'level': 1, 'xp': 0, 'xp_to_next': 100, 'con': 5, 'vit': 5},
      'streak': {'current_streak': 0, 'longest_streak': 0},
    };

/// A stand-in for Firebase. The opaque resolver handle is what makes this
/// possible: a real MultiFactorResolver cannot be constructed outside the SDK.
class FakeGateway implements SignInGateway {
  FakeGateway({
    this.signInOutcome = AuthOutcome.signedIn,
    this.failureMessage,
    this.smsSendFails = false,
    this.codeIsWrong = false,
  });

  AuthOutcome signInOutcome;
  String? failureMessage;
  bool smsSendFails;
  bool codeIsWrong;

  String? attemptedEmail;
  String? attemptedPassword;
  String? resetEmail;
  String? submittedCode;
  bool registered = false;
  int smsSends = 0;

  static const handle = 'opaque-resolver';

  @override
  Future<AuthStep> signIn({required String email, required String password}) async {
    attemptedEmail = email;
    attemptedPassword = password;
    if (signInOutcome == AuthOutcome.failed) {
      return AuthStep(AuthOutcome.failed,
          message: failureMessage ?? 'Incorrect email or password.');
    }
    if (signInOutcome == AuthOutcome.needsSmsCode) {
      return const AuthStep(AuthOutcome.needsSmsCode, resolver: handle);
    }
    return AuthStep(signInOutcome);
  }

  @override
  Future<AuthStep> register({required String email, required String password}) async {
    registered = true;
    attemptedEmail = email;
    if (signInOutcome == AuthOutcome.failed) {
      return AuthStep(AuthOutcome.failed,
          message: failureMessage ?? 'An account already exists for that email.');
    }
    return const AuthStep(AuthOutcome.needsEnrollment);
  }

  @override
  Future<AuthStep> sendSignInCode(Object resolver) async {
    smsSends++;
    if (smsSendFails) {
      return const AuthStep(AuthOutcome.failed, message: 'Could not send the code.');
    }
    return const AuthStep(AuthOutcome.needsSmsCode, verificationId: 'vid-1');
  }

  @override
  Future<AuthStep> submitSignInCode({
    required Object resolver,
    required String verificationId,
    required String smsCode,
  }) async {
    submittedCode = smsCode;
    if (codeIsWrong) {
      return const AuthStep(AuthOutcome.failed, message: 'That code is not correct.');
    }
    return const AuthStep(AuthOutcome.signedIn);
  }

  @override
  Future<void> sendPasswordReset(String email) async => resetEmail = email;
}

class FakeAuthenticator implements Authenticator {
  FakeAuthenticator({this.approves = true, this.supported = true});
  bool approves;
  bool supported;
  int prompts = 0;

  @override
  Future<bool> canCheck() async => supported;
  @override
  Future<List<BiometricType>> enrolled() async =>
      supported ? const [BiometricType.fingerprint] : const [];
  @override
  Future<bool> authenticate(String reason) async {
    prompts++;
    return approves;
  }
}

Future<SpriteAtlas> _fakeAtlas() async {
  final recorder = ui.PictureRecorder();
  Canvas(recorder).drawRect(
      const Rect.fromLTWH(0, 0, 512, 256), Paint()..color = const Color(0xFFFFFFFF));
  final picture = recorder.endRecording();
  final image = await picture.toImage(512, 256);
  picture.dispose();
  return SpriteAtlas.fromImages({'body_base': image});
}

Future<void> settle(WidgetTester tester, {int frames = 6}) async {
  for (var i = 0; i < frames; i++) {
    await tester.pump(const Duration(milliseconds: 32));
  }
}

Widget hostScreen(FakeGateway gateway, {VoidCallback? onSignedIn}) => MaterialApp(
      home: SignInScreen(
        gateway: gateway,
        onSignedIn: () async => onSignedIn?.call(),
      ),
    );

void main() {
  group('validation', () {
    testWidgets('an empty form does not reach the network', (tester) async {
      final gateway = FakeGateway();
      await tester.pumpWidget(hostScreen(gateway));

      await tester.tap(find.byKey(const Key('signin_submit')));
      await settle(tester);

      expect(gateway.attemptedEmail, isNull);
      expect(find.text('Enter your email address'), findsOneWidget);
    });

    testWidgets('a malformed email is caught locally', (tester) async {
      final gateway = FakeGateway();
      await tester.pumpWidget(hostScreen(gateway));

      await tester.enterText(find.byKey(const Key('signin_email')), 'not-an-email');
      await tester.enterText(find.byKey(const Key('signin_password')), 'hunter2!!');
      await tester.tap(find.byKey(const Key('signin_submit')));
      await settle(tester);

      expect(gateway.attemptedEmail, isNull);
      expect(find.textContaining('email address'), findsWidgets);
    });

    testWidgets('a short password is only rejected when registering',
        (tester) async {
      final gateway = FakeGateway();
      await tester.pumpWidget(hostScreen(gateway));

      // Signing in: an existing password predates whatever rule we pick today.
      await tester.enterText(find.byKey(const Key('signin_email')), 'a@b.com');
      await tester.enterText(find.byKey(const Key('signin_password')), 'short');
      await tester.tap(find.byKey(const Key('signin_submit')));
      await settle(tester);
      expect(gateway.attemptedEmail, 'a@b.com');

      // Registering: the rule applies.
      await tester.tap(find.byKey(const Key('signin_toggle_mode')));
      await settle(tester);
      await tester.enterText(find.byKey(const Key('signin_password')), 'short');
      await tester.tap(find.byKey(const Key('signin_submit')));
      await settle(tester);
      expect(gateway.registered, isFalse);
      expect(find.textContaining('at least 8'), findsOneWidget);
    });
  });

  group('sign in', () {
    testWidgets('valid credentials hand off to the app', (tester) async {
      final gateway = FakeGateway();
      var signedIn = false;
      await tester.pumpWidget(hostScreen(gateway, onSignedIn: () => signedIn = true));

      await tester.enterText(find.byKey(const Key('signin_email')), ' a@b.com ');
      await tester.enterText(find.byKey(const Key('signin_password')), 'hunter2!!');
      await tester.tap(find.byKey(const Key('signin_submit')));
      await settle(tester);

      expect(gateway.attemptedEmail, 'a@b.com', reason: 'email is trimmed');
      expect(signedIn, isTrue);
    });

    testWidgets('a wrong password shows the server wording and stays put',
        (tester) async {
      final gateway = FakeGateway(signInOutcome: AuthOutcome.failed);
      var signedIn = false;
      await tester.pumpWidget(hostScreen(gateway, onSignedIn: () => signedIn = true));

      await tester.enterText(find.byKey(const Key('signin_email')), 'a@b.com');
      await tester.enterText(find.byKey(const Key('signin_password')), 'wrong-one');
      await tester.tap(find.byKey(const Key('signin_submit')));
      await settle(tester);

      expect(signedIn, isFalse);
      expect(find.byKey(const Key('signin_error')), findsOneWidget);
      // Deliberately does not distinguish a wrong password from an unknown
      // address; doing so enumerates which emails have accounts.
      expect(find.text('Incorrect email or password.'), findsOneWidget);
    });

    testWidgets('registering routes onward for enrolment', (tester) async {
      final gateway = FakeGateway();
      var signedIn = false;
      await tester.pumpWidget(hostScreen(gateway, onSignedIn: () => signedIn = true));

      await tester.tap(find.byKey(const Key('signin_toggle_mode')));
      await settle(tester);
      await tester.enterText(find.byKey(const Key('signin_email')), 'new@b.com');
      await tester.enterText(find.byKey(const Key('signin_password')), 'longenough1');
      await tester.tap(find.byKey(const Key('signin_submit')));
      await settle(tester);

      expect(gateway.registered, isTrue);
      expect(signedIn, isTrue, reason: 'the server decides what happens next');
    });
  });

  group('the SMS challenge', () {
    testWidgets('an enrolled account is asked for a code', (tester) async {
      final gateway = FakeGateway(signInOutcome: AuthOutcome.needsSmsCode);
      var signedIn = false;
      await tester.pumpWidget(hostScreen(gateway, onSignedIn: () => signedIn = true));

      await tester.enterText(find.byKey(const Key('signin_email')), 'a@b.com');
      await tester.enterText(find.byKey(const Key('signin_password')), 'hunter2!!');
      await tester.tap(find.byKey(const Key('signin_submit')));
      await settle(tester);

      expect(gateway.smsSends, 1);
      expect(find.byKey(const Key('signin_code')), findsOneWidget);
      expect(find.byKey(const Key('signin_email')), findsNothing,
          reason: 'the credential step is done');
      expect(signedIn, isFalse, reason: 'not signed in until the code is right');

      await tester.enterText(find.byKey(const Key('signin_code')), '123456');
      await tester.tap(find.byKey(const Key('signin_submit')));
      await settle(tester);

      expect(gateway.submittedCode, '123456');
      expect(signedIn, isTrue);
    });

    testWidgets('a wrong code does not sign the user in', (tester) async {
      final gateway = FakeGateway(
          signInOutcome: AuthOutcome.needsSmsCode, codeIsWrong: true);
      var signedIn = false;
      await tester.pumpWidget(hostScreen(gateway, onSignedIn: () => signedIn = true));

      await tester.enterText(find.byKey(const Key('signin_email')), 'a@b.com');
      await tester.enterText(find.byKey(const Key('signin_password')), 'hunter2!!');
      await tester.tap(find.byKey(const Key('signin_submit')));
      await settle(tester);
      await tester.enterText(find.byKey(const Key('signin_code')), '000000');
      await tester.tap(find.byKey(const Key('signin_submit')));
      await settle(tester);

      expect(signedIn, isFalse);
      expect(find.byKey(const Key('signin_error')), findsOneWidget);
      expect(find.byKey(const Key('signin_code')), findsOneWidget,
          reason: 'the user should be able to retype it');
    });

    testWidgets('the user can back out to a different account', (tester) async {
      final gateway = FakeGateway(signInOutcome: AuthOutcome.needsSmsCode);
      await tester.pumpWidget(hostScreen(gateway));

      await tester.enterText(find.byKey(const Key('signin_email')), 'a@b.com');
      await tester.enterText(find.byKey(const Key('signin_password')), 'hunter2!!');
      await tester.tap(find.byKey(const Key('signin_submit')));
      await settle(tester);
      await tester.tap(find.byKey(const Key('signin_cancel_code')));
      await settle(tester);

      expect(find.byKey(const Key('signin_email')), findsOneWidget);
      expect(find.byKey(const Key('signin_code')), findsNothing);
    });

    testWidgets('a failed SMS send keeps the credential step', (tester) async {
      final gateway = FakeGateway(
          signInOutcome: AuthOutcome.needsSmsCode, smsSendFails: true);
      await tester.pumpWidget(hostScreen(gateway));

      await tester.enterText(find.byKey(const Key('signin_email')), 'a@b.com');
      await tester.enterText(find.byKey(const Key('signin_password')), 'hunter2!!');
      await tester.tap(find.byKey(const Key('signin_submit')));
      await settle(tester);

      expect(find.byKey(const Key('signin_code')), findsNothing);
      expect(find.byKey(const Key('signin_error')), findsOneWidget);
    });
  });

  group('password reset', () {
    testWidgets('needs an address first', (tester) async {
      final gateway = FakeGateway();
      await tester.pumpWidget(hostScreen(gateway));

      await tester.tap(find.byKey(const Key('signin_forgot')));
      await settle(tester);

      expect(gateway.resetEmail, isNull);
      expect(find.byKey(const Key('signin_error')), findsOneWidget);
    });

    testWidgets('never reveals whether the address has an account',
        (tester) async {
      final gateway = FakeGateway();
      await tester.pumpWidget(hostScreen(gateway));

      await tester.enterText(find.byKey(const Key('signin_email')), 'who@b.com');
      await tester.tap(find.byKey(const Key('signin_forgot')));
      await settle(tester);

      expect(gateway.resetEmail, 'who@b.com');
      expect(find.textContaining('If that address has an account'), findsOneWidget);
    });
  });

  group('app wiring', () {
    testWidgets('a signed-out app shows the sign-in screen', (tester) async {
      final tokens = StaticTokenProvider(null);
      await tester.pumpWidget(XPGainApp(
        controller: AppController(
          api: ApiClient(
              baseUrl: 'http://x',
              tokens: tokens,
              httpClient: MockClient((_) async => _json(_profileJson))),
          tokens: tokens,
        ),
        loadAtlas: _fakeAtlas,
        gateway: FakeGateway(),
      ));
      await settle(tester);

      expect(find.byKey(const Key('signin_email')), findsOneWidget);
    });

    testWidgets('signing in reaches the app', (tester) async {
      // The token appears only after the fake gateway "signs in", mirroring
      // Firebase populating currentUser.
      final tokens = StaticTokenProvider(null);
      final gateway = FakeGateway();

      await tester.pumpWidget(XPGainApp(
        controller: AppController(
          api: ApiClient(
              baseUrl: 'http://x',
              tokens: tokens,
              httpClient: MockClient((_) async => _json(_profileJson))),
          tokens: tokens,
        ),
        loadAtlas: _fakeAtlas,
        gateway: gateway,
      ));
      await settle(tester);

      tokens.setToken('now-signed-in');
      await tester.enterText(find.byKey(const Key('signin_email')), 'a@b.com');
      await tester.enterText(find.byKey(const Key('signin_password')), 'hunter2!!');
      await tester.tap(find.byKey(const Key('signin_submit')));
      await settle(tester);

      expect(find.text('Level 1'), findsOneWidget);
    });
  });

  group('the biometric lock', () {
    testWidgets('a declined prompt locks the app instead of opening it',
        (tester) async {
      final tokens = StaticTokenProvider('tok');
      final auth = FakeAuthenticator(approves: false);

      await tester.pumpWidget(XPGainApp(
        controller: AppController(
          api: ApiClient(
              baseUrl: 'http://x',
              tokens: tokens,
              httpClient: MockClient((_) async => _json(_profileJson))),
          tokens: tokens,
        ),
        loadAtlas: _fakeAtlas,
        gateway: FakeGateway(),
        biometricGate: BiometricGate(authenticator: auth),
      ));
      await settle(tester);

      expect(auth.prompts, 1);
      expect(find.byKey(const Key('locked_message')), findsOneWidget);
      expect(find.text('Level 1'), findsNothing);
    });

    testWidgets('an approved prompt opens the app', (tester) async {
      final tokens = StaticTokenProvider('tok');
      final auth = FakeAuthenticator();

      await tester.pumpWidget(XPGainApp(
        controller: AppController(
          api: ApiClient(
              baseUrl: 'http://x',
              tokens: tokens,
              httpClient: MockClient((_) async => _json(_profileJson))),
          tokens: tokens,
        ),
        loadAtlas: _fakeAtlas,
        gateway: FakeGateway(),
        biometricGate: BiometricGate(authenticator: auth),
      ));
      await settle(tester);

      expect(find.text('Level 1'), findsOneWidget);
    });

    testWidgets('a signed-out user is never prompted', (tester) async {
      // The property that keeps a fingerprint from acting as a login.
      final tokens = StaticTokenProvider(null);
      final auth = FakeAuthenticator();

      await tester.pumpWidget(XPGainApp(
        controller: AppController(
          api: ApiClient(
              baseUrl: 'http://x',
              tokens: tokens,
              httpClient: MockClient((_) async => _json(_profileJson))),
          tokens: tokens,
        ),
        loadAtlas: _fakeAtlas,
        gateway: FakeGateway(),
        biometricGate: BiometricGate(authenticator: auth),
      ));
      await settle(tester);

      expect(auth.prompts, 0, reason: 'there is no session to unlock');
      expect(find.byKey(const Key('signin_email')), findsOneWidget);
    });

    testWidgets('a device without biometrics still opens', (tester) async {
      final tokens = StaticTokenProvider('tok');
      final auth = FakeAuthenticator(supported: false);

      await tester.pumpWidget(XPGainApp(
        controller: AppController(
          api: ApiClient(
              baseUrl: 'http://x',
              tokens: tokens,
              httpClient: MockClient((_) async => _json(_profileJson))),
          tokens: tokens,
        ),
        loadAtlas: _fakeAtlas,
        gateway: FakeGateway(),
        biometricGate: BiometricGate(authenticator: auth),
      ));
      await settle(tester);

      expect(find.text('Level 1'), findsOneWidget);
    });
  });
}
