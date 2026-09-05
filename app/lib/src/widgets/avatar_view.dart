import 'package:flutter/material.dart';

import '../model/character_state.dart';
import '../render/animation_clock.dart';
import '../render/avatar_painter.dart';
import '../render/sprite_sheet.dart';

/// Renders the layered avatar and drives its animation.
///
/// Uses [SingleTickerProviderStateMixin], so the ticker is automatically muted
/// when the widget is off-screen or inside a disabled [TickerMode] -- the
/// avatar stops burning frames the moment it is scrolled out of view.
class AvatarView extends StatefulWidget {
  const AvatarView({
    super.key,
    required this.state,
    required this.atlas,
    this.pixelScale,
  });

  final CharacterState state;
  final SpriteAtlas atlas;
  final int? pixelScale;

  @override
  State<AvatarView> createState() => _AvatarViewState();
}

class _AvatarViewState extends State<AvatarView>
    with SingleTickerProviderStateMixin {
  late final AnimationClock _clock;

  @override
  void initState() {
    super.initState();
    _clock = AnimationClock(this)..start();
  }

  @override
  void didUpdateWidget(covariant AvatarView old) {
    super.didUpdateWidget(old);
    // A stance change restarts the clip from frame zero. Without this, swapping
    // from an 8-frame jog to a 4-frame idle would resume mid-cycle and the new
    // stance would appear to start on an arbitrary pose.
    if (widget.state.stance != old.state.stance) {
      _clock.reset();
    }
  }

  @override
  void dispose() {
    _clock.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return RepaintBoundary(
      // Isolates the avatar's per-frame repaints from the rest of the screen,
      // so an animating sprite does not dirty the stat panel beside it.
      child: CustomPaint(
        painter: AvatarPainter(
          state: widget.state,
          atlas: widget.atlas,
          clock: _clock,
          pixelScale: widget.pixelScale,
        ),
        size: Size.infinite,
      ),
    );
  }
}

/// A wardrobe row that toggles one layer's visibility.
///
/// The label deliberately shows the item's power level even while it is hidden,
/// which is the user-facing half of the guarantee: the number does not move
/// when the eye icon is tapped.
class LayerVisibilityTile extends StatelessWidget {
  const LayerVisibilityTile({
    super.key,
    required this.state,
    required this.layerId,
    required this.onChanged,
  });

  final CharacterState state;
  final String layerId;
  final ValueChanged<CharacterState> onChanged;

  @override
  Widget build(BuildContext context) {
    final layer = state.layers.firstWhere((l) => l.id == layerId);
    return ListTile(
      title: Text(layer.displayName.isEmpty ? layer.id : layer.displayName),
      subtitle: Text('Power ${layer.powerLevel}'),
      trailing: IconButton(
        icon: Icon(layer.isVisible ? Icons.visibility : Icons.visibility_off),
        tooltip: layer.isVisible ? 'Hide on avatar' : 'Show on avatar',
        onPressed: () => onChanged(state.toggleVisibility(layerId)),
      ),
    );
  }
}
