import 'package:flutter/material.dart';

import 'auth/auth_service.dart';
import 'auth/biometric_gate.dart';
import 'model/avatar_layer.dart';
import 'model/character_state.dart';
import 'render/sprite_sheet.dart';
import 'screens/home_screen.dart';
import 'screens/mfa_enroll_screen.dart';
import 'screens/onboarding_screen.dart';
import 'screens/sign_in_screen.dart';
import 'state/app_controller.dart';

/// Root widget. Chooses a screen from the controller's phase and nothing else,
/// so there is exactly one place that decides what the user is looking at.
class XPGainApp extends StatefulWidget {
  const XPGainApp({
    super.key,
    required this.controller,
    required this.loadAtlas,
    this.enroller,
    this.gateway,
    this.biometricGate,
  });

  final AppController controller;

  /// Supplied in a real build. When absent -- as in tests that never reach the
  /// multi-factor phase -- the enrolment screen is replaced by an explanation
  /// rather than constructing Firebase.
  final MfaEnroller? enroller;

  /// Supplied in a real build. Without it the signed-out screen can only offer
  /// a retry, since there is no identity provider to sign in against.
  final SignInGateway? gateway;

  /// Optional local unlock for an existing session. Never a way to create one.
  final BiometricGate? biometricGate;

  /// Injected so tests can supply a synthetic atlas instead of decoding assets.
  final Future<SpriteAtlas> Function() loadAtlas;

  @override
  State<XPGainApp> createState() => _XPGainAppState();
}

class _XPGainAppState extends State<XPGainApp> {
  SpriteAtlas? _atlas;

  /// True when a session exists but the device holder has not proved presence.
  bool _locked = false;

  static const _defaultAvatar = CharacterState(
    layers: [
      AvatarLayer(
        id: 'body',
        slot: EquipmentSlot.body,
        spriteSheetKey: 'body_base',
        powerLevel: 0,
        displayName: 'Base Body',
      ),
      AvatarLayer(
        id: 'torso',
        slot: EquipmentSlot.torso,
        spriteSheetKey: 'torso_plate',
        powerLevel: 55,
        displayName: 'Plate Cuirass',
      ),
      AvatarLayer(
        id: 'blade',
        slot: EquipmentSlot.weapon,
        spriteSheetKey: 'sword_steel',
        powerLevel: 75,
        displayName: 'Steel Blade',
      ),
    ],
  );

  @override
  void initState() {
    super.initState();
    widget.controller.addListener(_onControllerChanged);
    _boot();
  }

  Future<void> _boot() async {
    final atlas = await widget.loadAtlas();
    if (!mounted) return;
    setState(() => _atlas = atlas);

    // The gate runs before the session is used, and only when one exists. It
    // guards an existing session; it cannot produce one, so a signed-out user
    // is never prompted.
    final gate = widget.biometricGate;
    if (gate != null) {
      final hasSession = (await widget.controller.tokens.idToken()) != null;
      if (hasSession) {
        final result = await gate.unlock(hasSession: true);
        if (!gate.allows(result)) {
          if (mounted) setState(() => _locked = true);
          return;
        }
      }
    }

    await widget.controller.bootstrap();
  }

  Future<void> _retryUnlock() async {
    setState(() => _locked = false);
    await _boot();
  }

  void _onControllerChanged() {
    if (mounted) setState(() {});
  }

  @override
  void dispose() {
    widget.controller.removeListener(_onControllerChanged);
    _atlas?.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'XP_Gain',
      theme: ThemeData.dark(useMaterial3: true),
      home: _screenFor(widget.controller),
    );
  }

  Widget _screenFor(AppController c) {
    if (_locked) return _Locked(onRetry: _retryUnlock, onSignOut: c.signOut);

    switch (c.phase) {
      case AppPhase.loading:
        return const Scaffold(body: Center(child: CircularProgressIndicator()));

      case AppPhase.signedOut:
        final gateway = widget.gateway;
        if (gateway == null) {
          return _SignedOut(controller: c, message: c.errorMessage);
        }
        return SignInScreen(
          gateway: gateway,
          message: c.errorMessage,
          // Firebase has a session; only the server decides whether it is
          // sufficient, so re-bootstrap rather than assuming success.
          onSignedIn: c.bootstrap,
        );

      case AppPhase.mfaRequired:
        final enroller = widget.enroller;
        if (enroller == null) {
          return _Failed(controller: c);
        }
        return MfaEnrollScreen(
          enroller: enroller,
          message: c.errorMessage,
          onSignOut: c.signOut,
          // Re-bootstrap rather than assuming success: the fresh token now
          // carries the second-factor claim, and the server is the thing that
          // decides whether that is enough.
          onEnrolled: c.bootstrap,
        );

      case AppPhase.onboarding:
        return OnboardingScreen(controller: c);

      case AppPhase.ready:
        return HomeScreen(controller: c, atlas: _atlas, avatar: _defaultAvatar);

      case AppPhase.failed:
        return _Failed(controller: c);
    }
  }
}

class _SignedOut extends StatelessWidget {
  const _SignedOut({required this.controller, this.message});

  final AppController controller;
  final String? message;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: Center(
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const Text('Sign in to start training.'),
              if (message != null) ...[
                const SizedBox(height: 12),
                Text(message!,
                    key: const Key('signed_out_message'),
                    textAlign: TextAlign.center,
                    style: TextStyle(color: Theme.of(context).colorScheme.error)),
              ],
              const SizedBox(height: 20),
              // Sign-in is deliberately absent: it needs a configured Firebase
              // project, which this build does not have. See the README.
              FilledButton(
                key: const Key('retry_bootstrap'),
                onPressed: controller.bootstrap,
                child: const Text('Retry'),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

/// Shown when a session exists but the biometric prompt was declined.
///
/// Sign out is offered as the way past it: the session is still valid, so the
/// only honest alternatives are to prove presence or to end it.
class _Locked extends StatelessWidget {
  const _Locked({required this.onRetry, required this.onSignOut});

  final Future<void> Function() onRetry;
  final Future<void> Function() onSignOut;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: Center(
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const Icon(Icons.lock_outline, size: 48),
              const SizedBox(height: 16),
              const Text('XP_Gain is locked.', key: Key('locked_message')),
              const SizedBox(height: 20),
              FilledButton(
                key: const Key('locked_retry'),
                onPressed: onRetry,
                child: const Text('Unlock'),
              ),
              TextButton(
                key: const Key('locked_sign_out'),
                onPressed: onSignOut,
                child: const Text('Sign out instead'),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class _Failed extends StatelessWidget {
  const _Failed({required this.controller});

  final AppController controller;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: Center(
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              Text(controller.errorMessage ?? 'Something went wrong.',
                  key: const Key('failed_message'), textAlign: TextAlign.center),
              const SizedBox(height: 20),
              FilledButton(
                key: const Key('retry_bootstrap'),
                onPressed: controller.bootstrap,
                child: const Text('Try again'),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
