import 'package:local_auth/local_auth.dart';

/// What biometric unlock is, and is not.
///
/// This gates access to an **already-established** Firebase session on this
/// device. It is a convenience so a returning user does not retype a password,
/// and a local privacy screen so someone holding an unlocked phone cannot open
/// the app.
///
/// It is NOT a second authentication factor. A fingerprint produces no token
/// and sets no claim, so the server cannot verify one happened and does not
/// treat it as multi-factor -- see `auth.WithRequiredSecondFactor` on the Go
/// side. Crucially it also cannot *create* a session: if no Firebase session
/// exists, passing this gate grants nothing. Treating it as a login would mean
/// anyone who can add a fingerprint to the device owns the account.
abstract class Authenticator {
  Future<bool> canCheck();
  Future<List<BiometricType>> enrolled();
  Future<bool> authenticate(String reason);
}

/// Wraps the platform plugin.
class LocalAuthenticator implements Authenticator {
  LocalAuthenticator([LocalAuthentication? plugin])
      : _plugin = plugin ?? LocalAuthentication();

  final LocalAuthentication _plugin;

  @override
  Future<bool> canCheck() async {
    try {
      return await _plugin.canCheckBiometrics || await _plugin.isDeviceSupported();
    } on LocalAuthException {
      return false;
    }
  }

  @override
  Future<List<BiometricType>> enrolled() async {
    try {
      return await _plugin.getAvailableBiometrics();
    } on LocalAuthException {
      return const [];
    }
  }

  @override
  Future<bool> authenticate(String reason) async {
    try {
      return await _plugin.authenticate(
        localizedReason: reason,
        // Allow the device passcode as a fallback: refusing it would lock out
        // a user whose fingerprint stops reading, and the passcode is the same
        // trust level the biometric stands in for.
        biometricOnly: false,
        // Survives the app being backgrounded by the system prompt itself.
        persistAcrossBackgrounding: true,
      );
    } on LocalAuthException {
      // Every failure mode -- cancelled, timed out, no credentials enrolled --
      // means the same thing here: presence was not proved.
      return false;
    }
  }
}

/// Outcome of an unlock attempt.
enum UnlockResult {
  /// The user proved presence and the session may be used.
  unlocked,

  /// The device cannot do biometrics, or none are enrolled.
  unavailable,

  /// The prompt was shown and refused or cancelled.
  refused,
}

/// Decides whether the app should open straight into the session.
class BiometricGate {
  BiometricGate({required this.authenticator, this.required = false});

  final Authenticator authenticator;

  /// When false, an unavailable or refused biometric still lets a signed-in
  /// user through -- it is a convenience, not a lock. When true the app stays
  /// locked, which only makes sense once the user has opted in.
  final bool required;

  Future<bool> isAvailable() async {
    if (!await authenticator.canCheck()) return false;
    return (await authenticator.enrolled()).isNotEmpty;
  }

  /// Prompts for presence.
  ///
  /// [hasSession] is passed rather than assumed: with no Firebase session there
  /// is nothing to unlock, and prompting would imply the fingerprint could sign
  /// the user in. It cannot.
  Future<UnlockResult> unlock({required bool hasSession}) async {
    if (!hasSession) return UnlockResult.unavailable;
    if (!await isAvailable()) return UnlockResult.unavailable;

    final ok = await authenticator.authenticate(
      'Unlock XP_Gain to see your character',
    );
    return ok ? UnlockResult.unlocked : UnlockResult.refused;
  }

  /// Whether the app may proceed into the session after [result].
  bool allows(UnlockResult result) {
    switch (result) {
      case UnlockResult.unlocked:
        return true;
      case UnlockResult.unavailable:
        // A device with no biometric hardware must still be usable.
        return !required;
      case UnlockResult.refused:
        return false;
    }
  }
}
