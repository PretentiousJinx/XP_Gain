import 'dart:ui' as ui;

import 'package:flutter/material.dart';

import 'src/api/api_client.dart';
import 'src/api/token_provider.dart';
import 'src/app.dart';
import 'src/render/sprite_sheet.dart';
import 'src/state/app_controller.dart';

/// Server address, overridable at build time:
///
///   flutter run --dart-define=API_BASE_URL=http://10.0.2.2:8080
///
/// 10.0.2.2 is the host loopback as seen from the Android emulator; localhost
/// inside the emulator is the emulator itself.
const apiBaseUrl = String.fromEnvironment(
  'API_BASE_URL',
  defaultValue: 'http://localhost:8080',
);

/// A Firebase ID token for local development:
///
///   flutter run --dart-define=DEV_ID_TOKEN=<token>
///
/// Empty by default, which lands the app on the signed-out screen rather than
/// firing unauthenticated requests at the server.
const devIdToken = String.fromEnvironment('DEV_ID_TOKEN', defaultValue: '');

void main() {
  WidgetsFlutterBinding.ensureInitialized();

  // PLACEHOLDER credential source. Swap in a firebase_auth-backed
  // TokenProvider once the project is configured; nothing above this line
  // changes, because the API client only knows the interface.
  final TokenProvider tokens =
      devIdToken.isEmpty ? const NoTokenProvider() : StaticTokenProvider(devIdToken);

  final api = ApiClient(baseUrl: apiBaseUrl, tokens: tokens);

  runApp(XPGainApp(
    controller: AppController(api: api, tokens: tokens),
    loadAtlas: loadPlaceholderAtlas,
  ));
}

/// Dev-only: synthesises sprite sheets so the renderer runs before any art
/// exists. Replace with `SpriteAtlas.load({...})` once real sheets land in
/// assets/sprites/.
Future<SpriteAtlas> loadPlaceholderAtlas() async {
  const palette = <String, Color>{
    'body_base': Color(0xFF8D6E63),
    'legs_cloth': Color(0xFF455A64),
    'torso_plate': Color(0xFF90A4AE),
    'helm_iron': Color(0xFFCFD8DC),
    'sword_steel': Color(0xFFFFD54F),
  };

  const frameW = 64, frameH = 64, cols = 8, rows = 4;
  final images = <String, ui.Image>{};

  for (final entry in palette.entries) {
    final recorder = ui.PictureRecorder();
    final canvas = Canvas(recorder);
    final paint = Paint()..color = entry.value;

    for (var row = 0; row < rows; row++) {
      for (var col = 0; col < cols; col++) {
        final bob = (col % 4) - 1.5;
        canvas.drawRect(
          Rect.fromLTWH(col * frameW + 20.0, row * frameH + 16.0 + bob * 2, 24, 36),
          paint,
        );
      }
    }
    final picture = recorder.endRecording();
    images[entry.key] = await picture.toImage(frameW * cols, frameH * rows);
    picture.dispose();
  }

  return SpriteAtlas.fromImages(images,
      frameWidth: frameW, frameHeight: frameH, columns: cols);
}
