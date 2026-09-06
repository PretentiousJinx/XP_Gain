import 'dart:ui' as ui;

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart' show rootBundle;

import '../model/avatar_layer.dart';

/// A loaded sprite sheet: one image holding a grid of equally sized frames.
///
/// Every layer's sheet must share the same frame grid and the same registration
/// point, so that frame N of the torso lines up with frame N of the legs. That
/// convention is what allows the composite to be built by simply drawing each
/// sheet at the same destination rect, with no per-layer offset table.
@immutable
class SpriteSheet {
  const SpriteSheet({
    required this.image,
    required this.frameWidth,
    required this.frameHeight,
    required this.columns,
  });

  final ui.Image image;
  final int frameWidth;
  final int frameHeight;
  final int columns;

  /// Source rect for a frame at [row], [column].
  ///
  /// Returned in exact integer pixels. Any fractional source rect would make the
  /// sampler read a neighbouring frame's edge pixel and produce the one-pixel
  /// seam that plagues scaled pixel art.
  ui.Rect frameRect(int row, int column) {
    final x = (column % columns) * frameWidth;
    final y = row * frameHeight;
    return ui.Rect.fromLTWH(
      x.toDouble(),
      y.toDouble(),
      frameWidth.toDouble(),
      frameHeight.toDouble(),
    );
  }
}

/// Timing and layout for one stance within a sheet.
@immutable
class AnimationClip {
  const AnimationClip({
    required this.row,
    required this.frameCount,
    this.fps = 8,
    this.loop = true,
  });

  final int row;
  final int frameCount;
  final int fps;
  final bool loop;

  Duration get duration =>
      Duration(milliseconds: (frameCount * 1000 / fps).round());

  /// Frame index at [elapsed].
  ///
  /// Pixel-art animation is stepped, not interpolated: the frame index is a
  /// floor of elapsed time, so the sprite snaps between drawings the way the
  /// artist authored them. Easing it would produce sub-frame blending that
  /// reads as mush at this resolution.
  int frameAt(Duration elapsed) {
    if (frameCount <= 1) return 0;
    final totalMs = elapsed.inMilliseconds;
    final frameMs = 1000 ~/ fps;
    if (frameMs <= 0) return 0;
    final raw = totalMs ~/ frameMs;
    if (loop) return raw % frameCount;
    return raw >= frameCount ? frameCount - 1 : raw;
  }
}

/// The per-stance clip table. Each stance maps to one row of every sheet.
const Map<Stance, AnimationClip> kStanceClips = {
  Stance.idle: AnimationClip(row: 0, frameCount: 4, fps: 6),
  Stance.jogging: AnimationClip(row: 1, frameCount: 8, fps: 12),
  Stance.lifting: AnimationClip(row: 2, frameCount: 6, fps: 9),
  Stance.combat: AnimationClip(row: 3, frameCount: 6, fps: 14),
};

/// Holds every decoded sheet, keyed by [AvatarLayer.spriteSheetKey].
///
/// Decoding is done once up front rather than per frame: `ui.Image` decode is
/// asynchronous and allocating, and a `CustomPainter.paint` must be synchronous
/// and allocation-light. Any sheet missing at paint time is skipped rather than
/// awaited, so a slow-loading cosmetic never stalls the whole avatar.
class SpriteAtlas {
  SpriteAtlas._(this._sheets);

  final Map<String, SpriteSheet> _sheets;

  SpriteSheet? operator [](String key) => _sheets[key];
  bool contains(String key) => _sheets.containsKey(key);

  static Future<SpriteAtlas> load(
    Map<String, String> keyToAssetPath, {
    int frameWidth = 64,
    int frameHeight = 64,
    int columns = 8,
  }) async {
    final sheets = <String, SpriteSheet>{};
    for (final entry in keyToAssetPath.entries) {
      final data = await rootBundle.load(entry.value);
      final codec = await ui.instantiateImageCodec(
        data.buffer.asUint8List(),
      );
      final frame = await codec.getNextFrame();
      sheets[entry.key] = SpriteSheet(
        image: frame.image,
        frameWidth: frameWidth,
        frameHeight: frameHeight,
        columns: columns,
      );
    }
    return SpriteAtlas._(sheets);
  }

  /// Builds an atlas from already-decoded images. Used by tests and by the
  /// dev placeholder generator, which synthesise images rather than load them.
  factory SpriteAtlas.fromImages(
    Map<String, ui.Image> images, {
    int frameWidth = 64,
    int frameHeight = 64,
    int columns = 8,
  }) {
    return SpriteAtlas._({
      for (final e in images.entries)
        e.key: SpriteSheet(
          image: e.value,
          frameWidth: frameWidth,
          frameHeight: frameHeight,
          columns: columns,
        ),
    });
  }

  /// Releases every decoded image exactly once.
  ///
  /// Two keys may legitimately point at the same [ui.Image] -- an atlas built
  /// from one sheet reused across slots, or a test fixture. Disposing per entry
  /// would then free the same image twice, which trips an assertion inside
  /// dart:ui rather than failing quietly, so the identity set is load-bearing.
  void dispose() {
    final seen = Set<ui.Image>.identity();
    for (final sheet in _sheets.values) {
      if (seen.add(sheet.image)) sheet.image.dispose();
    }
    _sheets.clear();
  }
}
