import 'package:flutter/foundation.dart';
import 'package:flutter/scheduler.dart';

/// A ticking clock that drives repaints without rebuilding the widget tree.
///
/// Handing this to `CustomPainter(repaint: clock)` lets the painter be
/// re-invoked directly by the compositor. The alternative -- calling setState
/// on every tick -- would rebuild and re-lay-out the subtree at display rate to
/// change nothing but a source rect.
class AnimationClock extends ValueNotifier<Duration> {
  AnimationClock(TickerProvider vsync) : super(Duration.zero) {
    _ticker = vsync.createTicker(_onTick);
  }

  late final Ticker _ticker;

  /// Frames are only published when the value actually changes, so a paused
  /// clock costs nothing downstream.
  void _onTick(Duration elapsed) {
    if (elapsed != value) value = elapsed;
  }

  bool get isRunning => _ticker.isActive;

  void start() {
    if (!_ticker.isActive) _ticker.start();
  }

  /// Stops the ticker but keeps the accumulated time, so resuming does not
  /// snap the animation back to frame zero.
  void stop() {
    if (_ticker.isActive) _ticker.stop();
  }

  void reset() {
    value = Duration.zero;
  }

  @override
  void dispose() {
    _ticker.dispose();
    super.dispose();
  }
}
