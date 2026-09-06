import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:xp_gain/src/api/api_client.dart';
import 'package:xp_gain/src/api/models.dart';
import 'package:xp_gain/src/api/token_provider.dart';
import 'package:xp_gain/src/state/app_controller.dart';

http.Response _json(Object body, {int status = 200}) =>
    http.Response(jsonEncode(body), status,
        headers: {'content-type': 'application/json'});

Map<String, dynamic> profileJson({int kcal = 0}) => {
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
      'character': {'level': 1, 'xp': 0, 'xp_to_next': 100, 'con': 5, 'vit': 5},
      'streak': {'current_streak': 0, 'longest_streak': 0},
    };

Map<String, dynamic> intakeJson({int kcal = 650}) => {
      'entry_id': 'e1',
      'source': 'manual',
      'is_manual': true,
      'day_totals': {'kcal': kcal, 'protein_g': 45, 'carbs_g': 60, 'fat_g': 20},
      'remaining': {'kcal': 2200 - kcal, 'protein_g': 115, 'carbs_g': 160, 'fat_g': 50},
      'character': {'level': 2, 'xp': 10, 'xp_to_next': 200, 'con': 5, 'vit': 5},
      'streak': {'current_streak': 1, 'longest_streak': 1},
      'xp_awarded': 30,
      'levels_gained': 1,
    };

AppController controllerThat(
  Future<http.Response> Function(http.Request) handler, {
  TokenProvider? tokens,
}) {
  final t = tokens ?? StaticTokenProvider('tok');
  return AppController(
    api: ApiClient(
      baseUrl: 'http://x',
      tokens: t,
      httpClient: MockClient(handler),
    ),
    tokens: t,
  );
}

void main() {
  group('bootstrap', () {
    test('a provisioned account lands on the home screen', () async {
      final c = controllerThat((_) async => _json(profileJson(kcal: 700)));
      await c.bootstrap();

      expect(c.phase, AppPhase.ready);
      expect(c.profile?.dayTotals.kcal, 700);
    });

    test('a new account is routed to onboarding, not an error', () async {
      final c = controllerThat((_) async =>
          _json({'code': 'profile_not_found', 'message': 'nope'}, status: 404));
      await c.bootstrap();

      expect(c.phase, AppPhase.onboarding);
      expect(c.errorMessage, isNull,
          reason: 'a fresh account is an expected state, not a failure');
    });

    test('no credential means signed out without a request', () async {
      var called = false;
      final c = controllerThat(
        (_) async {
          called = true;
          return _json(profileJson());
        },
        tokens: const NoTokenProvider(),
      );
      await c.bootstrap();

      expect(c.phase, AppPhase.signedOut);
      expect(called, isFalse);
    });

    test('a revoked session signs out and explains why', () async {
      final c = controllerThat((_) async =>
          _json({'code': 'token_revoked', 'message': 'Please sign in again.'}, status: 401));
      await c.bootstrap();

      expect(c.phase, AppPhase.signedOut);
      expect(c.errorMessage, contains('sign in'));
    });

    test('a server outage surfaces as a failure the user can retry', () async {
      final c = controllerThat((_) async => _json({'code': 'internal_error'}, status: 500));
      await c.bootstrap();

      expect(c.phase, AppPhase.failed);
      expect(c.errorMessage, isNotNull);
    });
  });

  group('onboarding', () {
    test('saving goals provisions and moves to ready', () async {
      final c = controllerThat((req) async {
        if (req.method == 'PUT') return _json(profileJson(), status: 201);
        return _json({'code': 'profile_not_found'}, status: 404);
      });
      await c.bootstrap();
      expect(c.phase, AppPhase.onboarding);

      final ok = await c.saveProfile(timezone: 'UTC', goals: Goals.defaults);

      expect(ok, isTrue);
      expect(c.phase, AppPhase.ready);
    });

    test('rejected goals keep the user on the form with the reason', () async {
      final c = controllerThat((_) async => _json({
            'code': 'invalid_payload',
            'message': 'goal_kcal must be between 800 and 10000',
          }, status: 400));

      final ok = await c.saveProfile(
          timezone: 'UTC', goals: const Goals(kcal: 1, proteinG: 1, carbsG: 1, fatG: 1));

      expect(ok, isFalse);
      expect(c.errorMessage, contains('goal_kcal'));
      expect(c.phase, isNot(AppPhase.ready));
    });
  });

  group('logging food', () {
    test('an accepted entry updates the visible totals and stats', () async {
      final c = controllerThat((req) async {
        if (req.method == 'GET') return _json(profileJson());
        return _json(intakeJson(), status: 201);
      });
      await c.bootstrap();

      final ok = await c.logManual(const Macros(kcal: 650, proteinG: 45));

      expect(ok, isTrue);
      expect(c.profile?.dayTotals.kcal, 650);
      expect(c.profile?.character.level, 2);
      expect(c.profile?.streak.current, 1);
      expect(c.lastResult?.levelsGained, 1);
    });

    test('a rejected photo becomes a prompt, not an error', () async {
      final c = controllerThat((req) async {
        if (req.method == 'GET') return _json(profileJson());
        return _json({
          'code': 'photo_rejected',
          'validation_reasoning': 'That looks like a desk.',
          'rejection_id': 'rej_9',
        }, status: 422);
      });
      await c.bootstrap();

      final ok = await c.logPhoto(const VisionPayload(isValidFood: false));

      expect(ok, isFalse);
      expect(c.pendingRejection?.reasoning, contains('desk'));
      expect(c.pendingRejection?.rejectionId, 'rej_9');
      expect(c.errorMessage, isNull, reason: 'Path B is a branch, not a fault');
      expect(c.phase, AppPhase.ready, reason: 'the user stays on the home screen');
    });

    test('a manual override after a rejection links to it', () async {
      final bodies = <Map<String, dynamic>>[];
      final c = controllerThat((req) async {
        if (req.method == 'GET') return _json(profileJson());
        bodies.add(jsonDecode(req.body) as Map<String, dynamic>);
        if (req.url.path.endsWith('/photo')) {
          return _json({
            'code': 'photo_rejected',
            'validation_reasoning': 'blurry',
            'rejection_id': 'rej_9',
          }, status: 422);
        }
        return _json(intakeJson(), status: 201);
      });
      await c.bootstrap();

      await c.logPhoto(const VisionPayload(isValidFood: false));
      await c.logManual(const Macros(kcal: 650, proteinG: 45));

      expect(bodies.last['supersedes_rejection_id'], 'rej_9');
      expect(c.pendingRejection, isNull, reason: 'the rejection was answered');
    });
  });

  group('idempotency', () {
    test('a retry after a network failure reuses the same entry id', () async {
      // The property that stops a flaky connection from double-logging a meal
      // the server may already have recorded.
      final keys = <String>[];
      var attempt = 0;

      final c = controllerThat((req) async {
        if (req.method == 'GET') return _json(profileJson());
        keys.add((jsonDecode(req.body) as Map<String, dynamic>)['client_entry_id'] as String);
        attempt++;
        if (attempt == 1) throw http.ClientException('connection reset');
        return _json(intakeJson(), status: 201);
      });
      await c.bootstrap();

      final first = await c.logManual(const Macros(kcal: 650));
      expect(first, isFalse);
      expect(c.errorMessage, isNotNull);

      final second = await c.logManual(const Macros(kcal: 650));
      expect(second, isTrue);

      expect(keys, hasLength(2));
      expect(keys[0], keys[1],
          reason: 'a retry must reuse the key so the server can deduplicate');
    });

    test('a fresh entry after a success uses a new key', () async {
      final keys = <String>[];
      final c = controllerThat((req) async {
        if (req.method == 'GET') return _json(profileJson());
        keys.add((jsonDecode(req.body) as Map<String, dynamic>)['client_entry_id'] as String);
        return _json(intakeJson(), status: 201);
      });
      await c.bootstrap();

      await c.logManual(const Macros(kcal: 300));
      await c.logManual(const Macros(kcal: 400));

      expect(keys[0], isNot(keys[1]),
          reason: 'two real meals must not collapse into one entry');
    });

    test('a retaken photo uses a new key, since the old one was refused', () async {
      final keys = <String>[];
      var attempt = 0;
      final c = controllerThat((req) async {
        if (req.method == 'GET') return _json(profileJson());
        keys.add((jsonDecode(req.body) as Map<String, dynamic>)['client_entry_id'] as String);
        attempt++;
        if (attempt == 1) {
          return _json({
            'code': 'photo_rejected',
            'validation_reasoning': 'blurry',
            'rejection_id': 'rej_9',
          }, status: 422);
        }
        return _json(intakeJson(), status: 201);
      });
      await c.bootstrap();

      await c.logPhoto(const VisionPayload(isValidFood: false));
      await c.logPhoto(const VisionPayload(isValidFood: true, kcal: 600));

      expect(keys[0], isNot(keys[1]));
    });
  });

  test('signing out clears every trace of the session', () async {
    final c = controllerThat((req) async {
      if (req.method == 'GET') return _json(profileJson());
      return _json(intakeJson(), status: 201);
    });
    await c.bootstrap();
    await c.logManual(const Macros(kcal: 650));

    await c.signOut();

    expect(c.phase, AppPhase.signedOut);
    expect(c.profile, isNull);
    expect(c.lastResult, isNull);
    expect(c.pendingRejection, isNull);
  });
}
