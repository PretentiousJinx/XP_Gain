import 'package:flutter/foundation.dart';

/// Draw order for the avatar composite.
///
/// The enum's declaration order *is* the z-order, back to front. Encoding it in
/// the type rather than in a separate index field means a new slot cannot be
/// added without deciding where it sits in the stack, and no layer can ever
/// carry a z-index that contradicts another.
enum EquipmentSlot {
  backdrop,
  cape,
  body,
  legs,
  torso,
  arms,
  head,
  hair,
  offhand,
  weapon,
  aura,
}

/// The stances the avatar can be animated in.
enum Stance { idle, jogging, lifting, combat }

/// One independently-toggleable sprite layer.
///
/// A layer owns two things that must stay decoupled: how it *draws*
/// ([spriteSheetKey], [isVisible], [tint]) and what it *contributes*
/// ([powerLevel]). Hiding a helmet to show off a haircut is a cosmetic choice
/// and must never cost the player stats, so nothing in this class lets
/// visibility reach the power calculation.
@immutable
class AvatarLayer {
  const AvatarLayer({
    required this.id,
    required this.slot,
    required this.spriteSheetKey,
    required this.powerLevel,
    this.isVisible = true,
    this.isEquipped = true,
    this.tint,
    this.displayName = '',
  });

  final String id;
  final EquipmentSlot slot;

  /// Key into the loaded sprite-sheet atlas.
  final String spriteSheetKey;

  /// Contribution to Total Power Level. Read by the stat panel and by PvP
  /// matchmaking; deliberately independent of [isVisible].
  final int powerLevel;

  /// Cosmetic visibility only. False means "do not draw", never "unequip".
  final bool isVisible;

  /// Whether the item is actually equipped. This *does* gate power, because an
  /// unequipped item is not being worn at all.
  final bool isEquipped;

  final int? tint;
  final String displayName;

  /// True when this layer should contribute to Total Power Level.
  bool get countsTowardPower => isEquipped;

  /// True when the draw loop should emit this layer.
  bool get shouldDraw => isEquipped && isVisible;

  AvatarLayer copyWith({
    bool? isVisible,
    bool? isEquipped,
    int? powerLevel,
    int? tint,
  }) {
    return AvatarLayer(
      id: id,
      slot: slot,
      spriteSheetKey: spriteSheetKey,
      powerLevel: powerLevel ?? this.powerLevel,
      isVisible: isVisible ?? this.isVisible,
      isEquipped: isEquipped ?? this.isEquipped,
      tint: tint ?? this.tint,
      displayName: displayName,
    );
  }

  @override
  bool operator ==(Object other) =>
      other is AvatarLayer &&
      other.id == id &&
      other.slot == slot &&
      other.spriteSheetKey == spriteSheetKey &&
      other.powerLevel == powerLevel &&
      other.isVisible == isVisible &&
      other.isEquipped == isEquipped &&
      other.tint == tint;

  @override
  int get hashCode =>
      Object.hash(id, slot, spriteSheetKey, powerLevel, isVisible, isEquipped, tint);
}
