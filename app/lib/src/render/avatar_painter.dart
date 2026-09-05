import 'dart:ui' as ui;

import 'package:flutter/rendering.dart';

import '../model/avatar_layer.dart';
import '../model/character_state.dart';
import 'animation_clock.dart';
import 'sprite_sheet.dart';

/// Paints the layered pixel-art avatar.
///
/// The composite is built back-to-front from independent transparent sheets:
/// each layer is a PNG with alpha everywhere it does not draw, so stacking them
/// needs no masking and no per-layer geometry -- just N draws of the same
/// destination rect, in slot order.
class AvatarPainter extends CustomPainter {
  AvatarPainter({
    required this.state,
    required this.atlas,
    required this.clock,
    this.pixelScale,
  }) : super(repaint: clock);

  final CharacterState state;
  final SpriteAtlas atlas;

  /// Drives repaints directly, bypassing the widget rebuild path.
  final AnimationClock clock;

  /// Forced integer scale. When null, the largest integer scale that fits the
  /// available box is chosen at paint time.
  final int? pixelScale;

  /// Nearest-neighbour sampling with anti-aliasing off.
  ///
  /// One shared Paint for every layer of every frame: allocating one per layer
  /// inside paint() would churn the heap at display rate for no benefit.
  static final ui.Paint _pixelPaint = ui.Paint()
    ..filterQuality = ui.FilterQuality.none
    ..isAntiAlias = false;

  @override
  void paint(Canvas canvas, Size size) {
    final drawables = state.drawableLayers;
    if (drawables.isEmpty) return;

    final clip = kStanceClips[state.stance] ?? kStanceClips[Stance.idle]!;
    final frame = clip.frameAt(clock.value);

    // Every sheet shares a frame grid, so the first resolved sheet fixes the
    // geometry for the whole stack.
    SpriteSheet? reference;
    for (final layer in drawables) {
      final sheet = atlas[layer.spriteSheetKey];
      if (sheet != null) {
        reference = sheet;
        break;
      }
    }
    if (reference == null) return;

    final scale = pixelScale ?? _fitIntegerScale(size, reference);
    final destW = (reference.frameWidth * scale).toDouble();
    final destH = (reference.frameHeight * scale).toDouble();

    // Snap the origin to whole pixels. A half-pixel offset is invisible on a
    // photo and ruinous on pixel art: every edge shimmers as the sprite moves.
    final dx = ((size.width - destW) / 2).floorToDouble();
    final dy = ((size.height - destH) / 2).floorToDouble();
    final dest = ui.Rect.fromLTWH(dx, dy, destW, destH);

    canvas.save();
    if (state.facingLeft) {
      // Mirror about the sprite's own centre, not the canvas origin, so a flip
      // does not translate the character sideways.
      canvas.translate(dest.center.dx, 0);
      canvas.scale(-1, 1);
      canvas.translate(-dest.center.dx, 0);
    }

    for (final layer in drawables) {
      // ---- the visibility skip -----------------------------------------
      // drawableLayers already filtered on shouldDraw, so a hidden layer never
      // reaches this loop: no atlas lookup, no source rect, no draw call. Its
      // powerLevel is untouched in the model and is still summed by
      // CharacterState.totalPowerLevel.
      final sheet = atlas[layer.spriteSheetKey];
      if (sheet == null) continue; // sheet still decoding; skip it this frame

      final src = sheet.frameRect(clip.row, frame);
      canvas.drawImageRect(sheet.image, src, dest, _paintFor(layer));
    }

    canvas.restore();
  }

  ui.Paint _paintFor(AvatarLayer layer) {
    final tint = layer.tint;
    if (tint == null) return _pixelPaint;
    return ui.Paint()
      ..filterQuality = ui.FilterQuality.none
      ..isAntiAlias = false
      ..colorFilter = ui.ColorFilter.mode(ui.Color(tint), BlendMode.modulate);
  }

  /// Largest whole-number scale that still fits the frame in [size].
  ///
  /// Pixel art must be scaled by integers. At 2.37x the remainder lands
  /// unevenly -- some source pixels cover 2 device pixels, others 3 -- and the
  /// sprite visibly wobbles as it animates.
  static int _fitIntegerScale(Size size, SpriteSheet sheet) {
    final byWidth = size.width ~/ sheet.frameWidth;
    final byHeight = size.height ~/ sheet.frameHeight;
    final fit = byWidth < byHeight ? byWidth : byHeight;
    return fit < 1 ? 1 : fit;
  }

  @override
  bool shouldRepaint(covariant AvatarPainter old) {
    // The clock is already wired to `repaint`, so per-frame invalidation is
    // handled for us. This only needs to catch changes to the *inputs*.
    return state.stance != old.state.stance ||
        state.facingLeft != old.state.facingLeft ||
        !identical(state.layers, old.state.layers) ||
        !identical(atlas, old.atlas) ||
        pixelScale != old.pixelScale;
  }
}
