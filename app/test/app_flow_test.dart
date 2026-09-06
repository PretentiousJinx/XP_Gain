import 'dart:convert';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:xp_gain/src/api/api_client.dart';
import 'package:xp_gain/src/api/token_provider.dart';
import 'package:xp_gain/src/app.dart';
import 'package:xp_gain/src/render/sprite_sheet.dart';
import 'package:xp_gain/src/state/app_controller.dart';

http.Response _json(Object body, {int status = 200}) =>
    http.Response(jsonEncode(body), status,
        headers: {'content-type': 'application/json'});

Map<String, dynamic> _profile({int kcal = 0, int level = 1, int streak = 0}) => {
      'user_id': 'u1',
      'timezone': 'UTC',
      'goals': {
        'goal_kcal': 2200,
        'goal_protein_g': 160,
        'goal_carbs_g': 220,
        'goal_fat_g': 70
      },
      'local_date': '2026-09-05',
      'day_totals': {'kcal': kcal, 'protein_g': 0, 'carbs_g': 0, 'fat_g': 0},
      'remaining': {'kcal': 2200 - kcal, 'protein_g': 160, 'carbs_g': 220, 'fat_g': 70},
      'character': {'level': level, 'xp': 20, 'xp_to_next': 100, 'con': 7, 'vit': 6},
      'streak': {'current_streak': streak, 'longest_streak': streak},
    };

/// A one-frame atlas, so widget tests never decode assets.
Future<SpriteAtlas> _fakeAtlas() async {
  final recorder = ui.PictureRecorder();
  Canvas(recorder).drawRect(
      const Rect.fromLTWH(0, 0, 512, 256), Paint()..color = const Color(0xFFFFFFFF));
  final picture = recorder.endRecording();
  final image = await picture.toImage(512, 256);
  picture.dispose();
  return SpriteAtlas.fromImages({
    'body_base': image,
    'torso_plate': image,
    'sword_steel': image,
  });
}

Widget _app(Future<http.Response> Function(http.Request) handler,
    {TokenProvider? tokens}) {
  final t = tokens ?? StaticTokenProvider('tok');
  return XPGainApp(
    controller: AppController(
      api: ApiClient(baseUrl: 'http://x', tokens: t, httpClient: MockClient(handler)),
      tokens: t,
    ),
    loadAtlas: _fakeAtlas,
  );
}

/// Pumps a bounded number of frames.
///
/// `pumpAndSettle` cannot be used on any screen showing the avatar: its
/// AnimationClock ticks continuously by design, so the tree never goes quiet
/// and pumpAndSettle times out. That is correct app behaviour, not a defect,
/// so the tests advance a fixed number of frames instead of waiting for
/// quiescence.
Future<void> settle(WidgetTester tester, {int frames = 6}) async {
  for (var i = 0; i < frames; i++) {
    await tester.pump(const Duration(milliseconds: 32));
  }
}

void main() {
  testWidgets('a signed-out user is asked to sign in', (tester) async {
    await tester.pumpWidget(_app((_) async => _json(_profile()),
        tokens: const NoTokenProvider()));
    await settle(tester);

    expect(find.text('Sign in to start training.'), findsOneWidget);
  });

  testWidgets('a new account lands on onboarding and can provision', (tester) async {
    var provisioned = false;
    await tester.pumpWidget(_app((req) async {
      if (req.method == 'PUT') {
        provisioned = true;
        return _json(_profile(), status: 201);
      }
      return _json({'code': 'profile_not_found'}, status: 404);
    }));
    await settle(tester);

    expect(find.text('Set your daily targets'), findsOneWidget);

    await tester.tap(find.byKey(const Key('save_goals')));
    await settle(tester);

    expect(provisioned, isTrue);
    expect(find.text('Level 1'), findsOneWidget, reason: 'should be on the home screen');
  });

  testWidgets('rejected goals keep the user on the form and show the reason',
      (tester) async {
    await tester.pumpWidget(_app((req) async {
      if (req.method == 'PUT') {
        return _json({
          'code': 'invalid_payload',
          'message': 'goal_kcal must be between 800 and 10000',
        }, status: 400);
      }
      return _json({'code': 'profile_not_found'}, status: 404);
    }));
    await settle(tester);

    await tester.enterText(find.byKey(const Key('goal_kcal')), '1');
    await tester.tap(find.byKey(const Key('save_goals')));
    await settle(tester);

    expect(find.byKey(const Key('onboarding_error')), findsOneWidget);
    expect(find.textContaining('goal_kcal'), findsOneWidget);
    expect(find.text('Set your daily targets'), findsOneWidget);
  });

  testWidgets('the home screen renders the account state', (tester) async {
    await tester.pumpWidget(
        _app((_) async => _json(_profile(kcal: 700, level: 4, streak: 3))));
    await settle(tester);

    expect(find.text('Level 4'), findsOneWidget);
    expect(find.byKey(const Key('streak_label')), findsOneWidget);
    expect(find.text('3 day streak'), findsOneWidget);
    expect(find.byKey(const Key('con_label')), findsOneWidget);
    expect(find.text('CON 7'), findsOneWidget);
    expect(find.text('700 / 2200 kcal'), findsOneWidget);
  });

  testWidgets('a rejected photo shows the reasoning with both ways forward',
      (tester) async {
    await tester.pumpWidget(_app((req) async {
      if (req.method == 'GET') return _json(_profile());
      return _json({
        'code': 'photo_rejected',
        'validation_reasoning': 'This appears to be a photo of a desk, not a meal.',
        'rejection_id': 'rej_9',
        'can_retry_photo': true,
        'can_enter_manual': true,
      }, status: 422);
    }));
    await settle(tester);

    await tester.tap(find.byKey(const Key('log_photo')));
    await settle(tester);

    // The model's own words reach the user verbatim: they are the only thing
    // that says what to do differently.
    expect(find.byKey(const Key('rejection_reasoning')), findsOneWidget);
    expect(find.textContaining('desk'), findsOneWidget);
    expect(find.byKey(const Key('rejection_retake')), findsOneWidget);
    expect(find.byKey(const Key('rejection_manual')), findsOneWidget);
  });

  testWidgets('manual entry after a rejection updates the home screen',
      (tester) async {
    await tester.pumpWidget(_app((req) async {
      if (req.method == 'GET') return _json(_profile());
      if (req.url.path.endsWith('/photo')) {
        return _json({
          'code': 'photo_rejected',
          'validation_reasoning': 'too blurry',
          'rejection_id': 'rej_9',
        }, status: 422);
      }
      return _json({
        'entry_id': 'e1',
        'source': 'manual',
        'is_manual': true,
        'day_totals': {'kcal': 650, 'protein_g': 45, 'carbs_g': 60, 'fat_g': 20},
        'remaining': {'kcal': 1550, 'protein_g': 115, 'carbs_g': 160, 'fat_g': 50},
        'character': {'level': 1, 'xp': 30, 'xp_to_next': 100, 'con': 7, 'vit': 6},
        'streak': {'current_streak': 1, 'longest_streak': 1},
        'xp_awarded': 30,
        'levels_gained': 0,
      }, status: 201);
    }));
    await settle(tester);

    await tester.tap(find.byKey(const Key('log_photo')));
    await settle(tester);

    await tester.tap(find.byKey(const Key('rejection_manual')));
    await settle(tester);

    await tester.enterText(find.byKey(const Key('manual_kcal')), '650');
    await tester.enterText(find.byKey(const Key('manual_protein')), '45');
    await tester.tap(find.byKey(const Key('manual_save')));
    await settle(tester);

    expect(find.text('650 / 2200 kcal'), findsOneWidget);
    expect(find.text('1 day streak'), findsOneWidget);
  });

  testWidgets('a server outage offers a retry that recovers', (tester) async {
    var fail = true;
    await tester.pumpWidget(_app((_) async {
      if (fail) return _json({'code': 'internal_error'}, status: 500);
      return _json(_profile(level: 2));
    }));
    await settle(tester);

    expect(find.byKey(const Key('failed_message')), findsOneWidget);

    fail = false;
    await tester.tap(find.byKey(const Key('retry_bootstrap')));
    await settle(tester);

    expect(find.text('Level 2'), findsOneWidget);
  });
}
