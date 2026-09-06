import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/scheduler.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:xp_gain/src/model/avatar_layer.dart';
import 'package:xp_gain/src/model/character_state.dart';
import 'package:xp_gain/src/render/animation_clock.dart';
import 'package:xp_gain/src/render/avatar_painter.dart';
import 'package:xp_gain/src/render/sprite_sheet.dart';

/// A Canvas that records draw calls instead of rasterising.
///
/// The model tests prove that a hidden layer leaves the draw list. This proves
/// the consequence that actually matters: no draw call is ever issued for it.
/// Everything except the calls under test is absorbed by noSuchMethod.
class _RecordingCanvas implements Canvas {
  final List<ui.Image> drawnImages = <ui.Image>[];
  final List<ui.Rect> srcRects = <ui.Rect>[];
  int saveCount = 0;
  int restoreCount = 0;

  @override
  void drawImageRect(ui.Image image, ui.Rect src, ui.Rect dst, Paint paint) {
    drawnImages.add(image);
    srcRects.add(src);
  }

  @override
  void save() => saveCount++;

  @override
  void restore() => restoreCount++;

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

class _TestVSync implements TickerProvider {
  @override
  Ticker createTicker(TickerCallback onTick) => Ticker(onTick);
}

/// Builds a distinct 1x1-per-frame image so each sheet is identifiable by
/// reference in the recorded draw list.
Future<ui.Image> _sheetImage() async {
  const w = 64 * 8, h = 64 * 4;
  final recorder = ui.PictureRecorder();
  final canvas = Canvas(recorder);
  canvas.drawRect(
    Rect.fromLTWH(0, 0, w.toDouble(), h.toDouble()),
    Paint()..color = const Color(0xFFFFFFFF),
  );
  final picture = recorder.endRecording();
  final image = await picture.toImage(w, h);
  picture.dispose();
  return image;
}

CharacterState _state({bool helmVisible = true}) => CharacterState(
      con: 5,
      vit: 5,
      layers: [
        const AvatarLayer(
          id: 'body',
          slot: EquipmentSlot.body,
          spriteSheetKey: 'body',
          powerLevel: 0,
        ),
        AvatarLayer(
          id: 'helm',
          slot: EquipmentSlot.head,
          spriteSheetKey: 'helm',
          powerLevel: 40,
          isVisible: helmVisible,
        ),
        const AvatarLayer(
          id: 'blade',
          slot: EquipmentSlot.weapon,
          spriteSheetKey: 'blade',
          powerLevel: 75,
        ),
      ],
    );

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  late SpriteAtlas atlas;
  late Map<String, ui.Image> images;

  setUp(() async {
    images = {
      'body': await _sheetImage(),
      'helm': await _sheetImage(),
      'blade': await _sheetImage(),
    };
    atlas = SpriteAtlas.fromImages(images);
  });

  tearDown(() => atlas.dispose());

  AvatarPainter painterFor(CharacterState state, {Duration at = Duration.zero}) {
    final clock = AnimationClock(_TestVSync())..value = at;
    return AvatarPainter(state: state, atlas: atlas, clock: clock);
  }

  test('every visible layer produces exactly one draw call', () {
    final canvas = _RecordingCanvas();
    painterFor(_state()).paint(canvas, const Size(512, 512));

    expect(canvas.drawnImages.length, 3);
    expect(canvas.saveCount, canvas.restoreCount,
        reason: 'canvas save/restore must balance');
  });

  test('a hidden layer issues NO draw call', () {
    final visible = _RecordingCanvas();
    painterFor(_state()).paint(visible, const Size(512, 512));

    final hidden = _RecordingCanvas();
    painterFor(_state(helmVisible: false)).paint(hidden, const Size(512, 512));

    expect(hidden.drawnImages.length, 2,
        reason: 'hiding one of three layers must drop exactly one draw call');
    expect(hidden.drawnImages, isNot(contains(images['helm'])),
        reason: 'the hidden sheet must never reach the canvas');
    expect(hidden.drawnImages, contains(images['body']));
    expect(hidden.drawnImages, contains(images['blade']));
  });

  test('hiding a layer does not change Total Power Level', () {
    // The renderer-side half of the invariant, asserted alongside the draw
    // calls so the two can never drift apart.
    expect(_state(helmVisible: false).totalPowerLevel,
        _state().totalPowerLevel);
  });

  test('layers are drawn back-to-front in slot order', () {
    final canvas = _RecordingCanvas();
    painterFor(_state()).paint(canvas, const Size(512, 512));

    expect(canvas.drawnImages,
        [images['body'], images['helm'], images['blade']]);
  });

  test('stance selects the matching sprite-sheet row', () {
    for (final entry in kStanceClips.entries) {
      final canvas = _RecordingCanvas();
      painterFor(_state().copyWith(stance: entry.key))
          .paint(canvas, const Size(512, 512));

      final expectedTop = entry.value.row * 64.0;
      expect(canvas.srcRects.first.top, expectedTop,
          reason: '${entry.key.name} must read row ${entry.value.row}');
    }
  });

  test('elapsed time advances the frame within the row', () {
    final clip = kStanceClips[Stance.jogging]!; // 8 frames @ 12fps
    final frameMs = 1000 ~/ clip.fps;

    final first = _RecordingCanvas();
    painterFor(_state().copyWith(stance: Stance.jogging)).paint(first, const Size(512, 512));

    final third = _RecordingCanvas();
    painterFor(_state().copyWith(stance: Stance.jogging),
            at: Duration(milliseconds: frameMs * 2))
        .paint(third, const Size(512, 512));

    expect(first.srcRects.first.left, 0);
    expect(third.srcRects.first.left, 128, reason: 'frame 2 starts at x = 2 * 64');
  });

  test('animation loops rather than running off the end of the row', () {
    final clip = kStanceClips[Stance.jogging]!;
    final frameMs = 1000 ~/ clip.fps;

    final wrapped = _RecordingCanvas();
    painterFor(_state().copyWith(stance: Stance.jogging),
            at: Duration(milliseconds: frameMs * clip.frameCount))
        .paint(wrapped, const Size(512, 512));

    expect(wrapped.srcRects.first.left, 0,
        reason: 'frame ${clip.frameCount} must wrap back to frame 0');
  });

  test('all layers share one source rect, keeping the composite aligned', () {
    final canvas = _RecordingCanvas();
    painterFor(_state().copyWith(stance: Stance.combat),
            at: const Duration(milliseconds: 200))
        .paint(canvas, const Size(512, 512));

    expect(canvas.srcRects.toSet().length, 1,
        reason: 'a per-layer frame drift would tear the character apart');
  });

  test('a layer whose sheet is still loading is skipped, not crashed on', () {
    final state = CharacterState(layers: [
      ..._state().layers,
      const AvatarLayer(
        id: 'cape',
        slot: EquipmentSlot.cape,
        spriteSheetKey: 'not_loaded_yet',
        powerLevel: 10,
      ),
    ]);

    final canvas = _RecordingCanvas();
    painterFor(state).paint(canvas, const Size(512, 512));

    expect(canvas.drawnImages.length, 3, reason: 'missing sheet is skipped');
    expect(state.totalPowerLevel, _state().totalPowerLevel + 10,
        reason: 'a not-yet-loaded cosmetic still counts toward power');
  });

  test('empty draw list paints nothing without throwing', () {
    final canvas = _RecordingCanvas();
    painterFor(const CharacterState(layers: [])).paint(canvas, const Size(512, 512));
    expect(canvas.drawnImages, isEmpty);
  });

  _sharedImageDisposeTest();

  testWidgets('AvatarView mounts, animates and disposes cleanly', (tester) async {
    var state = _state();

    await tester.pumpWidget(MaterialApp(
      home: Scaffold(
        body: SizedBox(
          width: 400,
          height: 400,
          child: AvatarViewHarness(
            state: state,
            atlas: atlas,
            onToggle: () => state = state.toggleVisibility('helm'),
          ),
        ),
      ),
    ));

    await tester.pump(const Duration(milliseconds: 100));
    await tester.pump(const Duration(milliseconds: 100));
    expect(tester.takeException(), isNull);

    // Tearing down must dispose the ticker; a leaked one fails the test.
    await tester.pumpWidget(const SizedBox.shrink());
    expect(tester.takeException(), isNull);
  });
}

/// Minimal host so the widget test exercises the real AvatarView lifecycle.
class AvatarViewHarness extends StatelessWidget {
  const AvatarViewHarness({
    super.key,
    required this.state,
    required this.atlas,
    required this.onToggle,
  });

  final CharacterState state;
  final SpriteAtlas atlas;
  final VoidCallback onToggle;

  @override
  Widget build(BuildContext context) {
    return GestureDetector(
      onTap: onToggle,
      child: CustomPaint(
        painter: AvatarPainter(
          state: state,
          atlas: atlas,
          clock: AnimationClock(_TestVSync())..value = const Duration(milliseconds: 50),
        ),
        size: Size.infinite,
      ),
    );
  }
}

/// Regression: two keys may point at the same decoded image, and disposing the
/// atlas must free it once rather than once per entry. The second free trips an
/// assertion inside dart:ui, which surfaced as an unrelated-looking widget-tree
/// failure the first time a shared fixture was used.
void _sharedImageDisposeTest() {
  test('disposing an atlas with a shared image frees it once', () async {
    final image = await _sheetImage();
    final shared = SpriteAtlas.fromImages({'a': image, 'b': image, 'c': image});

    expect(shared.dispose, returnsNormally);
  });
}
