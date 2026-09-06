import 'dart:convert';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:xp_gain/src/api/api_client.dart';
import 'package:xp_gain/src/api/api_error.dart';
import 'package:xp_gain/src/api/token_provider.dart';
import 'package:xp_gain/src/app.dart';
import 'package:xp_gain/src/auth/auth_service.dart';
import 'package:xp_gain/src/render/sprite_sheet.dart';
import 'package:xp_gain/src/screens/mfa_enroll_screen.dart';
import 'package:xp_gain/src/state/app_controller.dart';

http.Response _json(Object body, {int status = 200}) =>
    http.Response(jsonEncode(body), status,
        headers: {'content-type': 'application/json'});

Map<String, dynamic> get _profile => {
      'user_id': 'u1',
      'timezone': 'UTC',
      'goals': {
        'goal_kcal': 2200,
        'goal_protein_g': 160,
        'goal_carbs_g': 220,
        'goal_fat_g': 70
      },
      'local_date': '2026-09-05',
      'day_totals': {'kcal': 0, 'protein_g': 0, 'carbs_g': 0, 'fat_g': 0},
      'remaining': {'kcal': 2200, 'protein_g': 160, 'carbs_g': 220, 'fat_g': 70},
      'character': {'level': 1, 'xp': 0, 'xp_to_next': 100, 'con': 5, 'vit': 5},
      'streak': {'current_streak': 0, 'longest_streak': 0},
    };

/// Drives the enrolment screen without a Firebase project.
class FakeEnroller implements MfaEnroller {
  FakeEnroller({this.sendFails = false, this.confirmFails = false});

  bool sendFails;
  bool confirmFails;
  String? sentTo;
  String? submittedCode;

  @override
  Future<AuthStep> startEnrollment(String phoneNumber) async {
    sentTo = phoneNumber;
    if (sendFails) {
      return const AuthStep(AuthOutcome.failed,
          message: 'That phone number is not valid. Include the country code.');
    }
    return const AuthStep(AuthOutcome.needsSmsCode, verificationId: 'vid-1');
  }

  @override
  Future<AuthStep> confirmEnrollment({
    required String verificationId,
    required String smsCode,
    String displayName = 'Phone',
  }) async {
    submittedCode = smsCode;
    if (confirmFails) {
      return const AuthStep(AuthOutcome.failed, message: 'That code is not correct.');
    }
    return const AuthStep(AuthOutcome.signedIn);
  }
}

Future<SpriteAtlas> _fakeAtlas() async {
  final recorder = ui.PictureRecorder();
  Canvas(recorder).drawRect(
      const Rect.fromLTWH(0, 0, 512, 256), Paint()..color = const Color(0xFFFFFFFF));
  final picture = recorder.endRecording();
  final image = await picture.toImage(512, 256);
  picture.dispose();
  return SpriteAtlas.fromImages({'body_base': image});
}

Future<void> settle(WidgetTester tester, {int frames = 6}) async {
  for (var i = 0; i < frames; i++) {
    await tester.pump(const Duration(milliseconds: 32));
  }
}

void main() {
  group('the 403 contract', () {
    test('mfa_required maps to MfaRequired without signing out', () async {
      // Signing out here would be a trap: enrolling a factor requires being
      // signed in, so dropping the session leaves the user unable to satisfy
      // the very requirement the server is asking for.
      final tokens = StaticTokenProvider('tok');
      final api = ApiClient(
        baseUrl: 'http://x',
        tokens: tokens,
        httpClient: MockClient((_) async => _json({
              'code': 'mfa_required',
              'message': 'Two-factor authentication is required on this account.',
              'needs_mfa_enrollment': true,
            }, status: 403)),
      );

      await expectLater(api.getProfile(), throwsA(isA<MfaRequired>()));
      expect(await tokens.idToken(), isNotNull,
          reason: 'the session is needed to enrol');
    });

    test('an unrecognised 403 still maps to MfaRequired', () async {
      final api = ApiClient(
        baseUrl: 'http://x',
        tokens: StaticTokenProvider('tok'),
        httpClient: MockClient((_) async => http.Response('nonsense', 403)),
      );
      await expectLater(api.getProfile(), throwsA(isA<MfaRequired>()));
    });

    test('the controller routes to the enrolment phase, keeping the session',
        () async {
      final tokens = StaticTokenProvider('tok');
      final c = AppController(
        api: ApiClient(
          baseUrl: 'http://x',
          tokens: tokens,
          httpClient: MockClient((_) async =>
              _json({'code': 'mfa_required', 'message': 'need 2fa'}, status: 403)),
        ),
        tokens: tokens,
      );

      await c.bootstrap();

      expect(c.phase, AppPhase.mfaRequired);
      expect(c.phase, isNot(AppPhase.signedOut));
      expect(await tokens.idToken(), isNotNull);
    });
  });

  group('enrolment screen', () {
    testWidgets('sends a code, then confirms it', (tester) async {
      final enroller = FakeEnroller();
      var enrolled = false;

      await tester.pumpWidget(MaterialApp(
        home: MfaEnrollScreen(
          enroller: enroller,
          onEnrolled: () async => enrolled = true,
        ),
      ));

      // Step 1: the number.
      expect(find.byKey(const Key('mfa_code')), findsNothing);
      await tester.enterText(find.byKey(const Key('mfa_phone')), '+15550100000');
      await tester.tap(find.byKey(const Key('mfa_submit')));
      await settle(tester);

      expect(enroller.sentTo, '+15550100000');
      expect(find.byKey(const Key('mfa_code')), findsOneWidget,
          reason: 'the code field appears only after an SMS is sent');

      // Step 2: the code.
      await tester.enterText(find.byKey(const Key('mfa_code')), '123456');
      await tester.tap(find.byKey(const Key('mfa_submit')));
      await settle(tester);

      expect(enroller.submittedCode, '123456');
      expect(enrolled, isTrue);
    });

    testWidgets('a bad phone number keeps the user on step one', (tester) async {
      final enroller = FakeEnroller(sendFails: true);

      await tester.pumpWidget(MaterialApp(
        home: MfaEnrollScreen(enroller: enroller, onEnrolled: () async {}),
      ));

      await tester.enterText(find.byKey(const Key('mfa_phone')), 'nope');
      await tester.tap(find.byKey(const Key('mfa_submit')));
      await settle(tester);

      expect(find.byKey(const Key('mfa_error')), findsOneWidget);
      expect(find.textContaining('country code'), findsOneWidget);
      expect(find.byKey(const Key('mfa_code')), findsNothing);
    });

    testWidgets('a wrong code does not complete enrolment', (tester) async {
      final enroller = FakeEnroller(confirmFails: true);
      var enrolled = false;

      await tester.pumpWidget(MaterialApp(
        home: MfaEnrollScreen(
          enroller: enroller,
          onEnrolled: () async => enrolled = true,
        ),
      ));

      await tester.enterText(find.byKey(const Key('mfa_phone')), '+15550100000');
      await tester.tap(find.byKey(const Key('mfa_submit')));
      await settle(tester);
      await tester.enterText(find.byKey(const Key('mfa_code')), '000000');
      await tester.tap(find.byKey(const Key('mfa_submit')));
      await settle(tester);

      expect(enrolled, isFalse);
      expect(find.byKey(const Key('mfa_error')), findsOneWidget);
    });

    testWidgets('an empty number is refused before any SMS is sent', (tester) async {
      final enroller = FakeEnroller();

      await tester.pumpWidget(MaterialApp(
        home: MfaEnrollScreen(enroller: enroller, onEnrolled: () async {}),
      ));

      await tester.tap(find.byKey(const Key('mfa_submit')));
      await settle(tester);

      expect(enroller.sentTo, isNull, reason: 'no point spending an SMS');
      expect(find.byKey(const Key('mfa_error')), findsOneWidget);
    });

    testWidgets('the user can go back and change the number', (tester) async {
      await tester.pumpWidget(MaterialApp(
        home: MfaEnrollScreen(enroller: FakeEnroller(), onEnrolled: () async {}),
      ));

      await tester.enterText(find.byKey(const Key('mfa_phone')), '+15550100000');
      await tester.tap(find.byKey(const Key('mfa_submit')));
      await settle(tester);
      expect(find.byKey(const Key('mfa_code')), findsOneWidget);

      await tester.tap(find.byKey(const Key('mfa_change_number')));
      await settle(tester);

      expect(find.byKey(const Key('mfa_code')), findsNothing);
    });
  });

  group('end to end', () {
    testWidgets('a 403 lands on enrolment, and enrolling reaches the app',
        (tester) async {
      var enrolledOnServer = false;
      final tokens = StaticTokenProvider('tok');

      final controller = AppController(
        api: ApiClient(
          baseUrl: 'http://x',
          tokens: tokens,
          httpClient: MockClient((_) async {
            // The server only accepts the session once a second factor exists.
            if (!enrolledOnServer) {
              return _json({'code': 'mfa_required', 'message': 'need 2fa'},
                  status: 403);
            }
            return _json(_profile);
          }),
        ),
        tokens: tokens,
      );

      final enroller = FakeEnroller();

      await tester.pumpWidget(XPGainApp(
        controller: controller,
        loadAtlas: _fakeAtlas,
        enroller: enroller,
      ));
      await settle(tester);

      expect(find.text('Secure your account'), findsOneWidget);

      await tester.enterText(find.byKey(const Key('mfa_phone')), '+15550100000');
      await tester.tap(find.byKey(const Key('mfa_submit')));
      await settle(tester);

      // Enrolling with Firebase is what makes the server change its mind.
      enrolledOnServer = true;

      await tester.enterText(find.byKey(const Key('mfa_code')), '123456');
      await tester.tap(find.byKey(const Key('mfa_submit')));
      await settle(tester);

      expect(find.text('Level 1'), findsOneWidget,
          reason: 'a re-bootstrap with the new claim should reach the home screen');
    });
  });
}
