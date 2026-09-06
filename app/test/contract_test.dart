import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:xp_gain/src/api/api_client.dart';
import 'package:xp_gain/src/api/api_error.dart';
import 'package:xp_gain/src/api/models.dart';
import 'package:xp_gain/src/api/token_provider.dart';

/// Parses real server responses committed under ../contract/.
///
/// Every other test in this suite asserts against JSON a human typed from
/// reading the Go structs, which cannot catch the failure that matters most:
/// a renamed field leaves both suites green and the app broken. These fixtures
/// are emitted by the Go server itself, so this file is the only place the two
/// halves of the project are checked against each other.
///
/// Regenerate with:
///   cd server && go test ./internal/httpapi/ -run TestGenerateContractFixtures

const _dir = '../contract';

Map<String, dynamic> load(String name) {
  final file = File('$_dir/$name');
  if (!file.existsSync()) {
    fail('missing contract fixture $name -- regenerate with the Go test');
  }
  return jsonDecode(file.readAsStringSync()) as Map<String, dynamic>;
}

/// Serves one fixture, so the whole client path (not just the model) is proven
/// against the server's real bytes.
ApiClient clientServing(String fixture, int status) {
  return ApiClient(
    baseUrl: 'http://x',
    tokens: StaticTokenProvider('tok'),
    httpClient: MockClient((_) async => http.Response(
          File('$_dir/$fixture').readAsStringSync(),
          status,
          headers: {'content-type': 'application/json'},
        )),
  );
}

void main() {
  test('fixtures exist', () {
    final dir = Directory(_dir);
    expect(dir.existsSync(), isTrue,
        reason: 'run the Go generator before the Dart contract tests');
    expect(dir.listSync().whereType<File>().length, greaterThanOrEqualTo(9));
  });

  group('profile', () {
    test('a freshly provisioned account parses', () async {
      final api = clientServing('profile_created.json', 201);
      final p = await api.putProfile(timezone: 'UTC', goals: Goals.defaults);

      expect(p.userId, isNotEmpty);
      expect(p.timezone, isNotEmpty);
      expect(p.goals.kcal, 2200);
      expect(p.goals.proteinG, 160);
      expect(p.character.level, 1);
      expect(p.character.con, 5, reason: 'starting Constitution');
      expect(p.streak.current, 0);
      expect(p.created, isTrue, reason: 'the server flags a first provision');
    });

    test('an active account parses, including totals and streak', () async {
      final api = clientServing('profile_active.json', 200);
      final p = await api.getProfile();

      expect(p.goals.kcal, 2200);
      // Two entries were logged when the fixture was generated.
      expect(p.dayTotals.kcal, greaterThan(0));
      expect(p.remaining.kcal, 2200 - p.dayTotals.kcal);
      expect(p.streak.current, 1);
      expect(p.localDate, matches(RegExp(r'^\d{4}-\d{2}-\d{2}$')));
    });
  });

  group('intake', () {
    test('a manual entry parses and keeps its pedigree', () async {
      final api = clientServing('intake_manual.json', 201);
      final r = await api.submitManual(
          clientEntryId: 'c1', macros: const Macros(kcal: 650));

      expect(r.entryId, isNotEmpty);
      expect(r.source, 'manual');
      expect(r.isManual, isTrue,
          reason: 'the flag the PvP layer reads must survive the round trip');
      expect(r.dayTotals.kcal, 650);
      expect(r.xpAwarded, greaterThan(0));
    });

    test('a photo entry parses and is not flagged manual', () async {
      final api = clientServing('intake_photo.json', 201);
      final r = await api.submitPhoto(
          clientEntryId: 'c2', vision: const VisionPayload(isValidFood: true));

      expect(r.source, 'photo');
      expect(r.isManual, isFalse);
      expect(r.character.level, greaterThanOrEqualTo(1));
      expect(r.streak.current, greaterThanOrEqualTo(1));
    });
  });

  group('errors', () {
    test('a rejected photo maps to PhotoRejected with the real reasoning', () async {
      final api = clientServing('error_photo_rejected.json', 422);

      try {
        await api.submitPhoto(
            clientEntryId: 'c3', vision: const VisionPayload(isValidFood: false));
        fail('expected PhotoRejected');
      } on PhotoRejected catch (e) {
        expect(e.reasoning, contains('desk'),
            reason: 'the model\'s own words must reach the user unaltered');
        expect(e.rejectionId, isNotEmpty,
            reason: 'needed to link a retry or manual override to this refusal');
        expect(e.canEnterManual, isTrue);
        expect(e.canRetryPhoto, isTrue);
      }
    });

    test('an unprovisioned account maps to NeedsOnboarding', () async {
      final api = clientServing('error_profile_not_found.json', 404);
      await expectLater(api.getProfile(), throwsA(isA<NeedsOnboarding>()));
    });

    test('rejected goals map to InvalidRequest naming the field', () async {
      final api = clientServing('error_invalid_payload.json', 400);

      try {
        await api.putProfile(timezone: 'UTC', goals: Goals.defaults);
        fail('expected InvalidRequest');
      } on InvalidRequest catch (e) {
        expect(e.message, contains('goal_kcal'));
      }
    });

    test('a single-factor session maps to MfaRequired', () async {
      final api = clientServing('error_mfa_required.json', 403);
      await expectLater(api.getProfile(), throwsA(isA<MfaRequired>()));
    });

    test('a missing credential maps to Unauthorized', () async {
      final api = clientServing('error_unauthorized.json', 401);
      await expectLater(api.getProfile(), throwsA(isA<Unauthorized>()));
    });
  });

  group('field names', () {
    // Asserts on raw keys, so a rename on the server fails here loudly rather
    // than silently becoming a default value in a parsed model.
    test('profile keys are the ones the client reads', () {
      final j = load('profile_active.json');
      for (final key in [
        'user_id', 'timezone', 'goals', 'local_date',
        'day_totals', 'remaining', 'character', 'streak',
      ]) {
        expect(j.containsKey(key), isTrue, reason: 'profile lost key "$key"');
      }
      expect((j['goals'] as Map).containsKey('goal_kcal'), isTrue);
      expect((j['character'] as Map).keys,
          containsAll(['level', 'xp', 'xp_to_next', 'con', 'vit']));
      expect((j['streak'] as Map).containsKey('current_streak'), isTrue);
    });

    test('intake keys are the ones the client reads', () {
      final j = load('intake_manual.json');
      expect(j.keys, containsAll([
        'entry_id', 'source', 'is_manual', 'day_totals',
        'remaining', 'character', 'streak', 'xp_awarded', 'levels_gained',
      ]));
    });

    test('the rejection body keys are the ones the client reads', () {
      final j = load('error_photo_rejected.json');
      expect(j.keys, containsAll([
        'code', 'message', 'validation_reasoning', 'rejection_id',
        'can_retry_photo', 'can_enter_manual',
      ]));
      expect(j['code'], 'photo_rejected');
    });

    test('the MFA body keys are the ones the client reads', () {
      final j = load('error_mfa_required.json');
      expect(j['code'], 'mfa_required');
      expect(j['needs_mfa_enrollment'], true);
    });

    test('error codes the client switches on are stable', () {
      expect(load('error_profile_not_found.json')['code'], 'profile_not_found');
      expect(load('error_invalid_payload.json')['code'], 'invalid_payload');
      expect(load('error_unauthorized.json')['code'], 'unauthorized');
      expect(load('error_profile_not_found.json')['needs_onboarding'], true);
    });
  });
}
