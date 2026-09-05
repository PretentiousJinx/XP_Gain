import 'package:flutter/foundation.dart';

import 'avatar_layer.dart';

/// The full avatar model: base stats plus the layer stack.
///
/// This is the single source of truth the renderer reads and the stat panel
/// reads. Because both read the same object, a layer that is hidden on screen
/// is still present in the model, which is exactly what keeps Total Power Level
/// stable across a visibility toggle.
@immutable
class CharacterState {
  const CharacterState({
    required this.layers,
    this.stance = Stance.idle,
    this.level = 1,
    this.xp = 0,
    this.xpToNext = 100,
    this.con = 5,
    this.vit = 5,
    this.facingLeft = false,
  });

  final List<AvatarLayer> layers;
  final Stance stance;
  final int level;
  final int xp;
  final int xpToNext;
  final int con;
  final int vit;
  final bool facingLeft;

  /// Total Power Level.
  ///
  /// Sums every *equipped* layer, visible or not. This getter is the reason the
  /// visibility toggle is safe: the draw loop filters on [AvatarLayer.shouldDraw]
  /// while this filters on [AvatarLayer.countsTowardPower], so hiding a layer
  /// changes only what is painted. Filtering both on the same flag is the bug
  /// this design exists to prevent -- players would lose power by transmogging.
  int get totalPowerLevel {
    var total = 0;
    for (final layer in layers) {
      if (layer.countsTowardPower) total += layer.powerLevel;
    }
    return total + con + vit;
  }

  /// Layers the renderer should draw, sorted back-to-front.
  ///
  /// Sorting by the slot's declaration index gives a stable, total order that
  /// does not depend on the order layers happen to arrive from the server.
  List<AvatarLayer> get drawableLayers {
    final visible = layers.where((l) => l.shouldDraw).toList()
      ..sort((a, b) => a.slot.index.compareTo(b.slot.index));
    return visible;
  }

  /// Toggles one layer's visibility, leaving every other property untouched.
  CharacterState toggleVisibility(String layerId) {
    return copyWith(
      layers: [
        for (final l in layers)
          if (l.id == layerId) l.copyWith(isVisible: !l.isVisible) else l,
      ],
    );
  }

  CharacterState copyWith({
    List<AvatarLayer>? layers,
    Stance? stance,
    int? level,
    int? xp,
    int? xpToNext,
    int? con,
    int? vit,
    bool? facingLeft,
  }) {
    return CharacterState(
      layers: layers ?? this.layers,
      stance: stance ?? this.stance,
      level: level ?? this.level,
      xp: xp ?? this.xp,
      xpToNext: xpToNext ?? this.xpToNext,
      con: con ?? this.con,
      vit: vit ?? this.vit,
      facingLeft: facingLeft ?? this.facingLeft,
    );
  }
}
