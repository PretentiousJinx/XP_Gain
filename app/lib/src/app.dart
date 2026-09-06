import 'package:flutter/material.dart';

import 'model/avatar_layer.dart';
import 'model/character_state.dart';
import 'render/sprite_sheet.dart';
import 'screens/home_screen.dart';
import 'screens/onboarding_screen.dart';
import 'state/app_controller.dart';

/// Root widget. Chooses a screen from the controller's phase and nothing else,
/// so there is exactly one place that decides what the user is looking at.
class XPGainApp extends StatefulWidget {
  const XPGainApp({
    super.key,
    required this.controller,
    required this.loadAtlas,
  });

  final AppController controller;

  /// Injected so tests can supply a synthetic atlas instead of decoding assets.
  final Future<SpriteAtlas> Function() loadAtlas;

  @override
  State<XPGainApp> createState() => _XPGainAppState();
}

class _XPGainAppState extends State<XPGainApp> {
  SpriteAtlas? _atlas;

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
    await widget.controller.bootstrap();
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
    switch (c.phase) {
      case AppPhase.loading:
        return const Scaffold(body: Center(child: CircularProgressIndicator()));

      case AppPhase.signedOut:
        return _SignedOut(controller: c, message: c.errorMessage);

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
