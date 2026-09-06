import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:xp_gain/src/api/api_client.dart';
import 'package:xp_gain/src/api/api_error.dart';
import 'package:xp_gain/src/api/models.dart';
import 'package:xp_gain/src/api/token_provider.dart';

const _base = 'http://localhost:8080';

/// Captures the requests the client makes so tests can assert on the wire.
class Recorder {
  final List<http.Request> requests = [];
}

ApiClient clientThat(
  Future<http.Response> Function(http.Request req) handler, {
  TokenProvider? tokens,
  Recorder? recorder,
}) {
  return ApiClient(
    baseUrl: _base,
    tokens: tokens ?? StaticTokenProvider('id-token'),
    httpClient: MockClient((req) async {
      recorder?.requests.add(req);
      return handler(req);
    }),
  );
}

http.Response _json(Object body, {int status = 200}) =>
    http.Response(jsonEncode(body), status,
        headers: {'content-type': 'application/json'});

Map<String, dynamic> get _profileJson => {
      'user_id': 'u1',
      'timezone': 'America/Los_Angeles',
      'goals': {
        'goal_kcal': 2200,
        'goal_protein_g': 160,
        'goal_carbs_g': 220,
        'goal_fat_g': 70,
      },
      'local_date': '2026-09-05',
      'day_totals': {'kcal': 700, 'protein_g': 50, 'carbs_g': 60, 'fat_g': 20},
      'remaining': {'kcal': 1500, 'protein_g': 110, 'carbs_g': 160, 'fat_g': 50},
      'character': {'level': 3, 'xp': 40, 'xp_to_next': 300, 'con': 7, 'vit': 6},
      'streak': {'current_streak': 4, 'longest_streak': 9, 'last_local_date': '2026-09-05'},
      'created': false,
    };

void main() {
  group('requests', () {
    test('attaches the bearer token', () async {
      final rec = Recorder();
      final api = clientThat((_) async => _json(_profileJson), recorder: rec);

      await api.getProfile();

      expect(rec.requests.single.headers['authorization'], 'Bearer id-token');
    });

    test('refuses to send without a token, without touching the network', () async {
      var called = false;
      final api = clientThat(
        (_) async {
          called = true;
          return _json(_profileJson);
        },
        tokens: const NoTokenProvider(),
      );

      await expectLater(api.getProfile(), throwsA(isA<Unauthorized>()));
      expect(called, isFalse, reason: 'no point spending a request with no credential');
    });

    test('manual submit carries the idempotency key and rejection link', () async {
      final rec = Recorder();
      final api = clientThat((_) async => _json({'entry_id': 'e1'}, status: 201),
          recorder: rec);

      await api.submitManual(
        clientEntryId: 'stable-key',
        macros: const Macros(kcal: 650, proteinG: 45, carbsG: 60, fatG: 20),
        supersedesRejectionId: 'rej_7',
      );

      final body = jsonDecode(rec.requests.single.body) as Map<String, dynamic>;
      expect(body['client_entry_id'], 'stable-key');
      expect(body['kcal'], 650);
      expect(body['supersedes_rejection_id'], 'rej_7');
    });

    test('omits the rejection link when there is none', () async {
      final rec = Recorder();
      final api = clientThat((_) async => _json({'entry_id': 'e1'}, status: 201),
          recorder: rec);

      await api.submitManual(
        clientEntryId: 'k',
        macros: const Macros(kcal: 100),
      );

      final body = jsonDecode(rec.requests.single.body) as Map<String, dynamic>;
      expect(body.containsKey('supersedes_rejection_id'), isFalse);
    });

    test('profile PUT flattens goals to the server field names', () async {
      final rec = Recorder();
      final api = clientThat((_) async => _json(_profileJson, status: 201), recorder: rec);

      await api.putProfile(timezone: 'UTC', goals: Goals.defaults);

      final body = jsonDecode(rec.requests.single.body) as Map<String, dynamic>;
      expect(body['timezone'], 'UTC');
      expect(body['goal_kcal'], 2200);
      expect(body['goal_protein_g'], 160);
      expect(body.containsKey('goals'), isFalse, reason: 'goals are flattened, not nested');
    });
  });

  group('parsing', () {
    test('reads a profile', () async {
      final api = clientThat((_) async => _json(_profileJson));
      final p = await api.getProfile();

      expect(p.userId, 'u1');
      expect(p.goals.kcal, 2200);
      expect(p.dayTotals.kcal, 700);
      expect(p.remaining.proteinG, 110);
      expect(p.character.level, 3);
      expect(p.character.xpFraction, closeTo(40 / 300, 0.001));
      expect(p.streak.current, 4);
    });

    test('survives a response missing fields', () async {
      // Version skew between a deployed API and an installed app must degrade,
      // not crash on launch.
      final api = clientThat((_) async => _json({'user_id': 'u1'}));
      final p = await api.getProfile();

      expect(p.userId, 'u1');
      expect(p.dayTotals, const Macros());
      expect(p.character.level, 1);
    });

    test('survives a non-JSON body', () async {
      final api = clientThat((_) async => http.Response('<html>502</html>', 200));
      final p = await api.getProfile();
      expect(p.userId, isEmpty);
    });
  });

  group('error mapping', () {
    test('404 profile_not_found becomes NeedsOnboarding', () async {
      final api = clientThat((_) async => _json(
            {'code': 'profile_not_found', 'message': 'not set up', 'needs_onboarding': true},
            status: 404,
          ));

      await expectLater(api.getProfile(), throwsA(isA<NeedsOnboarding>()));
    });

    test('422 photo_rejected carries the reasoning and rejection id', () async {
      final api = clientThat((_) async => _json({
            'code': 'photo_rejected',
            'message': 'not accepted',
            'validation_reasoning': 'This appears to be a photo of a desk, not a meal.',
            'rejection_id': 'rej_42',
            'can_retry_photo': true,
            'can_enter_manual': true,
            'confidence': 0.2,
          }, status: 422));

      try {
        await api.submitPhoto(
          clientEntryId: 'k',
          vision: const VisionPayload(isValidFood: false),
        );
        fail('expected PhotoRejected');
      } on PhotoRejected catch (e) {
        expect(e.reasoning, contains('desk'));
        expect(e.rejectionId, 'rej_42');
        expect(e.canEnterManual, isTrue);
        expect(e.confidence, 0.2);
      }
    });

    test('invalid_payload becomes InvalidRequest with the server message', () async {
      final api = clientThat((_) async => _json(
            {'code': 'invalid_payload', 'message': 'goal_kcal must be between 800 and 10000'},
            status: 400,
          ));

      try {
        await api.putProfile(timezone: 'UTC', goals: Goals.defaults);
        fail('expected InvalidRequest');
      } on InvalidRequest catch (e) {
        expect(e.message, contains('goal_kcal'));
      }
    });

    test('5xx becomes ServerFailure, not a network error', () async {
      final api = clientThat((_) async => _json({'code': 'internal_error'}, status: 500));
      await expectLater(api.getProfile(), throwsA(isA<ServerFailure>()));
    });

    test('a dead connection becomes NetworkFailure', () async {
      final api = clientThat((_) async => throw http.ClientException('connection refused'));
      await expectLater(api.getProfile(), throwsA(isA<NetworkFailure>()));
    });
  });

  group('token lifecycle', () {
    test('an expired token is refreshed and the request retried once', () async {
      final tokens = StaticTokenProvider('id-token');
      var attempts = 0;

      final api = clientThat((_) async {
        attempts++;
        if (attempts == 1) {
          return _json({'code': 'token_expired', 'message': 'expired'}, status: 401);
        }
        return _json(_profileJson);
      }, tokens: tokens);

      final p = await api.getProfile();

      expect(p.userId, 'u1');
      expect(attempts, 2, reason: 'should retry exactly once');
      expect(tokens.refreshCount, 1, reason: 'the retry must force a refresh');
    });

    test('a persistently expired token does not loop', () async {
      final tokens = StaticTokenProvider('id-token');
      var attempts = 0;

      final api = clientThat((_) async {
        attempts++;
        return _json({'code': 'token_expired', 'message': 'expired'}, status: 401);
      }, tokens: tokens);

      await expectLater(api.getProfile(), throwsA(isA<TokenExpired>()));
      expect(attempts, 2, reason: 'one original plus one retry, then give up');
    });

    test('a revoked session signs out and is not retried', () async {
      final tokens = StaticTokenProvider('id-token');
      var attempts = 0;

      final api = clientThat((_) async {
        attempts++;
        return _json({'code': 'token_revoked', 'message': 'revoked'}, status: 401);
      }, tokens: tokens);

      await expectLater(api.getProfile(), throwsA(isA<SessionRevoked>()));
      expect(attempts, 1, reason: 'refreshing cannot recover a revoked session');
      expect(await tokens.idToken(), isNull, reason: 'the dead session was dropped');
    });
  });
}
