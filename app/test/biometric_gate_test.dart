import 'package:flutter_test/flutter_test.dart';
import 'package:local_auth/local_auth.dart';
import 'package:xp_gain/src/auth/biometric_gate.dart';

class FakeAuthenticator implements Authenticator {
  FakeAuthenticator({
    this.supported = true,
    this.types = const [BiometricType.fingerprint],
    this.approves = true,
  });

  bool supported;
  List<BiometricType> types;
  bool approves;
  int prompts = 0;

  @override
  Future<bool> canCheck() async => supported;

  @override
  Future<List<BiometricType>> enrolled() async => types;

  @override
  Future<bool> authenticate(String reason) async {
    prompts++;
    return approves;
  }
}

void main() {
  group('availability', () {
    test('needs both hardware and an enrolled biometric', () async {
      expect(
        await BiometricGate(authenticator: FakeAuthenticator()).isAvailable(),
        isTrue,
      );
      expect(
        await BiometricGate(authenticator: FakeAuthenticator(supported: false))
            .isAvailable(),
        isFalse,
      );
      // Hardware present but nothing enrolled is the common case on a new phone.
      expect(
        await BiometricGate(authenticator: FakeAuthenticator(types: const []))
            .isAvailable(),
        isFalse,
      );
    });
  });

  group('unlocking', () {
    test('an approved prompt unlocks', () async {
      final gate = BiometricGate(authenticator: FakeAuthenticator());
      expect(await gate.unlock(hasSession: true), UnlockResult.unlocked);
    });

    test('a refused prompt does not', () async {
      final gate =
          BiometricGate(authenticator: FakeAuthenticator(approves: false));
      expect(await gate.unlock(hasSession: true), UnlockResult.refused);
    });

    test('no session means no prompt at all', () async {
      // The security property: a fingerprint cannot *create* a session. If it
      // prompted here it would imply biometrics can sign a user in, and anyone
      // who can add a fingerprint to the device would own the account.
      final auth = FakeAuthenticator();
      final gate = BiometricGate(authenticator: auth);

      expect(await gate.unlock(hasSession: false), UnlockResult.unavailable);
      expect(auth.prompts, 0, reason: 'nothing to unlock, so nothing was asked');
    });

    test('a device without biometrics is not prompted', () async {
      final auth = FakeAuthenticator(supported: false);
      final gate = BiometricGate(authenticator: auth);

      expect(await gate.unlock(hasSession: true), UnlockResult.unavailable);
      expect(auth.prompts, 0);
    });
  });

  group('policy', () {
    test('by default a device without biometrics still works', () async {
      final gate = BiometricGate(authenticator: FakeAuthenticator());
      expect(gate.allows(UnlockResult.unavailable), isTrue,
          reason: 'an optional convenience must not lock out old hardware');
      expect(gate.allows(UnlockResult.unlocked), isTrue);
    });

    test('a refusal always blocks, even when optional', () async {
      final gate = BiometricGate(authenticator: FakeAuthenticator());
      expect(gate.allows(UnlockResult.refused), isFalse,
          reason: 'someone actively declined the prompt');
    });

    test('when required, unavailable also blocks', () async {
      final gate =
          BiometricGate(authenticator: FakeAuthenticator(), required: true);
      expect(gate.allows(UnlockResult.unavailable), isFalse);
      expect(gate.allows(UnlockResult.refused), isFalse);
      expect(gate.allows(UnlockResult.unlocked), isTrue);
    });
  });

  test('the gate holds no credential of its own', () {
    // Documents the boundary this class exists to keep. BiometricGate has no
    // token, no user id and no way to reach the API: it can only say whether
    // the app may open a session that Firebase already established. The server
    // never learns a biometric prompt happened, and must not, because a client
    // could simply claim it did.
    final gate = BiometricGate(authenticator: FakeAuthenticator());
    expect(gate.authenticator, isA<Authenticator>());
    expect(gate.required, isFalse);
  });
}
