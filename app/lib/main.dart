import 'dart:ui' as ui;

import 'package:flutter/material.dart';

import 'src/model/avatar_layer.dart';
import 'src/model/character_state.dart';
import 'src/render/sprite_sheet.dart';
import 'src/widgets/avatar_view.dart';

void main() => runApp(const XPGainApp());

class XPGainApp extends StatelessWidget {
  const XPGainApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'XP_Gain',
      theme: ThemeData.dark(useMaterial3: true),
      home: const AvatarScreen(),
    );
  }
}

class AvatarScreen extends StatefulWidget {
  const AvatarScreen({super.key});

  @override
  State<AvatarScreen> createState() => _AvatarScreenState();
}

class _AvatarScreenState extends State<AvatarScreen> {
  SpriteAtlas? _atlas;

  CharacterState _state = const CharacterState(
    level: 7,
    con: 12,
    vit: 9,
    layers: [
      AvatarLayer(
        id: 'body',
        slot: EquipmentSlot.body,
        spriteSheetKey: 'body_base',
        powerLevel: 0,
        displayName: 'Base Body',
      ),
      AvatarLayer(
        id: 'legs',
        slot: EquipmentSlot.legs,
        spriteSheetKey: 'legs_cloth',
        powerLevel: 15,
        displayName: 'Cloth Leggings',
      ),
      AvatarLayer(
        id: 'torso',
        slot: EquipmentSlot.torso,
        spriteSheetKey: 'torso_plate',
        powerLevel: 55,
        displayName: 'Plate Cuirass',
      ),
      AvatarLayer(
        id: 'helm',
        slot: EquipmentSlot.head,
        spriteSheetKey: 'helm_iron',
        powerLevel: 40,
        displayName: 'Iron Helm',
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
    _loadAtlas();
  }

  Future<void> _loadAtlas() async {
    // Swap this for SpriteAtlas.load({...}) once real sheets land in
    // assets/sprites/. The placeholder keeps the pipeline runnable before the
    // art exists, so layering and animation can be verified independently of it.
    final atlas = await _DevPlaceholderAtlas.build(
      const {
        'body_base': Color(0xFF8D6E63),
        'legs_cloth': Color(0xFF455A64),
        'torso_plate': Color(0xFF90A4AE),
        'helm_iron': Color(0xFFCFD8DC),
        'sword_steel': Color(0xFFFFD54F),
      },
    );
    if (mounted) setState(() => _atlas = atlas);
  }

  @override
  void dispose() {
    _atlas?.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final atlas = _atlas;
    return Scaffold(
      appBar: AppBar(
        title: Text('Level ${_state.level}  ~  Power ${_state.totalPowerLevel}'),
      ),
      body: atlas == null
          ? const Center(child: CircularProgressIndicator())
          : Column(
              children: [
                Expanded(
                  flex: 3,
                  child: AvatarView(state: _state, atlas: atlas),
                ),
                _StanceBar(
                  current: _state.stance,
                  onPick: (s) => setState(() => _state = _state.copyWith(stance: s)),
                ),
                Expanded(
                  flex: 2,
                  child: ListView(
                    children: [
                      for (final layer in _state.layers)
                        LayerVisibilityTile(
                          state: _state,
                          layerId: layer.id,
                          onChanged: (next) => setState(() => _state = next),
                        ),
                    ],
                  ),
                ),
              ],
            ),
    );
  }
}

class _StanceBar extends StatelessWidget {
  const _StanceBar({required this.current, required this.onPick});

  final Stance current;
  final ValueChanged<Stance> onPick;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 8),
      child: Wrap(
        spacing: 8,
        alignment: WrapAlignment.center,
        children: [
          for (final s in Stance.values)
            ChoiceChip(
              label: Text(s.name),
              selected: s == current,
              onSelected: (_) => onPick(s),
            ),
        ],
      ),
    );
  }
}

/// Dev-only: synthesises a 4-row sprite sheet per layer so the renderer can be
/// exercised before any art exists. Delete once real sheets ship.
class _DevPlaceholderAtlas {
  static Future<SpriteAtlas> build(Map<String, Color> keyToColor) async {
    const frameW = 64, frameH = 64, cols = 8, rows = 4;
    final images = <String, ui.Image>{};

    for (final e in keyToColor.entries) {
      final recorder = ui.PictureRecorder();
      final canvas = Canvas(recorder);
      final paint = Paint()..color = e.value;

      for (var row = 0; row < rows; row++) {
        for (var col = 0; col < cols; col++) {
          // A blob that shifts by frame, so animation is visibly running.
          final bob = (col % 4) - 1.5;
          canvas.drawRect(
            Rect.fromLTWH(
              col * frameW + 20.0,
              row * frameH + 16.0 + bob * 2,
              24,
              36,
            ),
            paint,
          );
        }
      }
      final picture = recorder.endRecording();
      images[e.key] = await picture.toImage(frameW * cols, frameH * rows);
      picture.dispose();
    }
    return SpriteAtlas.fromImages(images,
        frameWidth: frameW, frameHeight: frameH, columns: cols);
  }
}
