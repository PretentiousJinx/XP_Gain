import 'package:flutter_test/flutter_test.dart';
import 'package:xp_gain/src/model/avatar_layer.dart';
import 'package:xp_gain/src/model/character_state.dart';

CharacterState _fixture() => const CharacterState(
      con: 5,
      vit: 5,
      layers: [
        AvatarLayer(
          id: 'body',
          slot: EquipmentSlot.body,
          spriteSheetKey: 'body_base',
          powerLevel: 0,
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

void main() {
  test('hiding a layer does not change Total Power Level', () {
    final before = _fixture();
    final after = before.toggleVisibility('helm');

    expect(after.layers.firstWhere((l) => l.id == 'helm').isVisible, isFalse);
    expect(after.totalPowerLevel, before.totalPowerLevel,
        reason: 'transmog must be purely cosmetic');
  });

  test('hiding a layer removes it from the draw list', () {
    final before = _fixture();
    expect(before.drawableLayers.map((l) => l.id), containsAll(['helm', 'blade']));

    final after = before.toggleVisibility('helm');
    expect(after.drawableLayers.map((l) => l.id), isNot(contains('helm')));
    expect(after.drawableLayers.map((l) => l.id), contains('blade'));
  });

  test('unequipping DOES remove power, unlike hiding', () {
    final before = _fixture();
    final unequipped = before.copyWith(
      layers: [
        for (final l in before.layers)
          if (l.id == 'blade') l.copyWith(isEquipped: false) else l,
      ],
    );

    expect(unequipped.totalPowerLevel, before.totalPowerLevel - 75);
    expect(unequipped.drawableLayers.map((l) => l.id), isNot(contains('blade')));
  });

  test('draw order follows slot declaration order, not list order', () {
    const scrambled = CharacterState(layers: [
      AvatarLayer(
          id: 'w', slot: EquipmentSlot.weapon, spriteSheetKey: 'w', powerLevel: 1),
      AvatarLayer(
          id: 'b', slot: EquipmentSlot.body, spriteSheetKey: 'b', powerLevel: 1),
      AvatarLayer(
          id: 'h', slot: EquipmentSlot.head, spriteSheetKey: 'h', powerLevel: 1),
    ]);

    expect(scrambled.drawableLayers.map((l) => l.id).toList(), ['b', 'h', 'w']);
  });

  test('toggling is reversible and leaves other layers untouched', () {
    final before = _fixture();
    final roundTrip = before.toggleVisibility('helm').toggleVisibility('helm');

    expect(roundTrip.drawableLayers.map((l) => l.id).toList(),
        before.drawableLayers.map((l) => l.id).toList());
    expect(roundTrip.totalPowerLevel, before.totalPowerLevel);
  });
}
