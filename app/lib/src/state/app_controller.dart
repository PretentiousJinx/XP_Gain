import 'dart:math';

import 'package:flutter/foundation.dart';

import '../api/api_client.dart';
import '../api/api_error.dart';
import '../api/models.dart';
import '../api/token_provider.dart';

/// Where the app is in its lifecycle.
enum AppPhase { loading, signedOut, onboarding, ready, failed }

/// Owns all server-backed state and the transitions between screens.
///
/// The UI reads from here and never touches [ApiClient] directly, so error
/// mapping and idempotency live in exactly one place.
class AppController extends ChangeNotifier {
  AppController({required this.api, required this.tokens});

  final ApiClient api;
  final TokenProvider tokens;

  AppPhase phase = AppPhase.loading;
  Profile? profile;
  String? errorMessage;

  /// Set when the Vision AI declined a photo. The UI shows the reasoning and
  /// offers a retake or manual entry; both paths reference [rejectionId] so the
  /// server can link the eventual entry to this refusal.
  PhotoRejected? pendingRejection;

  /// Result of the most recent accepted log, for the reward animation.
  IntakeResult? lastResult;

  bool busy = false;

  /// The idempotency key for the submission in flight.
  ///
  /// Held across retries on purpose: the server deduplicates on this value, so
  /// minting a fresh one after a timeout would double-log a meal the server may
  /// well have already recorded.
  String? _pendingEntryId;

  static String newEntryId() {
    final rand = Random();
    final suffix = List.generate(8, (_) => rand.nextInt(16).toRadixString(16)).join();
    return '${DateTime.now().microsecondsSinceEpoch.toRadixString(16)}-$suffix';
  }

  /// Decides the first screen: onboarding for a new account, home otherwise.
  Future<void> bootstrap() async {
    _set(() {
      phase = AppPhase.loading;
      errorMessage = null;
    });

    final token = await tokens.idToken();
    if (token == null || token.isEmpty) {
      _set(() => phase = AppPhase.signedOut);
      return;
    }

    try {
      profile = await api.getProfile();
      _set(() => phase = AppPhase.ready);
    } on NeedsOnboarding {
      // Expected for a brand-new account, not an error worth showing.
      _set(() => phase = AppPhase.onboarding);
    } on SessionRevoked catch (e) {
      _set(() {
        phase = AppPhase.signedOut;
        errorMessage = e.message;
      });
    } on Unauthorized {
      _set(() => phase = AppPhase.signedOut);
    } on ApiException catch (e) {
      _set(() {
        phase = AppPhase.failed;
        errorMessage = e.message;
      });
    }
  }

  /// Provisions the account, or saves changed goals.
  Future<bool> saveProfile({required String timezone, required Goals goals}) async {
    return _guard(() async {
      profile = await api.putProfile(timezone: timezone, goals: goals);
      phase = AppPhase.ready;
      return true;
    });
  }

  /// Logs a user-typed entry, optionally answering a rejected photo.
  Future<bool> logManual(Macros macros) async {
    final entryId = _pendingEntryId ??= newEntryId();
    return _guard(() async {
      final result = await api.submitManual(
        clientEntryId: entryId,
        macros: macros,
        supersedesRejectionId: pendingRejection?.rejectionId,
      );
      _applyResult(result);
      return true;
    });
  }

  /// Submits parsed Vision AI output.
  ///
  /// A [PhotoRejected] is captured into [pendingRejection] rather than surfaced
  /// as an error, because it is a branch of the workflow the user can act on,
  /// not a failure.
  Future<bool> logPhoto(VisionPayload vision, {String photoUri = ''}) async {
    final entryId = _pendingEntryId ??= newEntryId();
    final supersedes = pendingRejection?.rejectionId;

    return _guard(() async {
      try {
        final result = await api.submitPhoto(
          clientEntryId: entryId,
          vision: vision,
          photoUri: photoUri,
          supersedesRejectionId: supersedes,
        );
        _applyResult(result);
        return true;
      } on PhotoRejected catch (rejection) {
        // A new attempt needs a new idempotency key; the old one is spent on a
        // submission the server refused.
        _pendingEntryId = null;
        pendingRejection = rejection;
        return false;
      }
    });
  }

  /// Discards a pending rejection, e.g. when the user abandons the retry.
  void clearRejection() => _set(() {
        pendingRejection = null;
        _pendingEntryId = null;
      });

  void clearLastResult() => _set(() => lastResult = null);

  Future<void> signOut() async {
    await tokens.signOut();
    _set(() {
      profile = null;
      pendingRejection = null;
      lastResult = null;
      _pendingEntryId = null;
      phase = AppPhase.signedOut;
    });
  }

  void _applyResult(IntakeResult result) {
    lastResult = result;
    pendingRejection = null;
    _pendingEntryId = null; // this submission is done; the next one is new

    final p = profile;
    if (p != null) {
      profile = Profile(
        userId: p.userId,
        timezone: p.timezone,
        goals: p.goals,
        localDate: p.localDate,
        dayTotals: result.dayTotals,
        remaining: result.remaining,
        character: result.character,
        streak: result.streak,
      );
    }
  }

  /// Runs an action with busy tracking and uniform error mapping.
  Future<bool> _guard(Future<bool> Function() action) async {
    _set(() {
      busy = true;
      errorMessage = null;
    });
    try {
      return await action();
    } on NeedsOnboarding {
      _set(() => phase = AppPhase.onboarding);
      return false;
    } on SessionRevoked catch (e) {
      _set(() {
        phase = AppPhase.signedOut;
        errorMessage = e.message;
      });
      return false;
    } on Unauthorized catch (e) {
      _set(() {
        phase = AppPhase.signedOut;
        errorMessage = e.message;
      });
      return false;
    } on ApiException catch (e) {
      _set(() => errorMessage = e.message);
      return false;
    } finally {
      _set(() => busy = false);
    }
  }

  void _set(void Function() mutate) {
    mutate();
    notifyListeners();
  }
}
