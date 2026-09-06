import 'dart:async';
import 'dart:convert';
import 'dart:io' show SocketException;

import 'package:http/http.dart' as http;

import 'api_error.dart';
import 'models.dart';
import 'token_provider.dart';

/// Talks to the XP_Gain Go service.
///
/// Every method attaches the Firebase ID token, maps the server's error codes
/// onto the typed exceptions in api_error.dart, and retries exactly once on an
/// expired token. Nothing above this layer sees an HTTP status code.
class ApiClient {
  ApiClient({
    required this.baseUrl,
    required this.tokens,
    http.Client? httpClient,
    this.timeout = const Duration(seconds: 20),
  }) : _http = httpClient ?? http.Client();

  final String baseUrl;
  final TokenProvider tokens;
  final Duration timeout;
  final http.Client _http;

  void close() => _http.close();

  Future<Profile> getProfile() async {
    final body = await _send('GET', '/v1/me');
    return Profile.fromJson(body);
  }

  Future<Profile> putProfile({
    required String timezone,
    required Goals goals,
  }) async {
    final body = await _send('PUT', '/v1/me', body: {
      'timezone': timezone,
      ...goals.toJson(),
    });
    return Profile.fromJson(body);
  }

  /// Logs a user-typed entry.
  ///
  /// [clientEntryId] is the idempotency key and **must be stable across
  /// retries** of the same logical submission. Generating a fresh one on retry
  /// would double-log the meal, which is exactly the failure the server's
  /// idempotency check exists to prevent.
  Future<IntakeResult> submitManual({
    required String clientEntryId,
    required Macros macros,
    String? supersedesRejectionId,
  }) async {
    final body = await _send('POST', '/v1/intake/manual', body: {
      'client_entry_id': clientEntryId,
      ...macros.toJson(),
      if (supersedesRejectionId != null)
        'supersedes_rejection_id': supersedesRejectionId,
    });
    return IntakeResult.fromJson(body);
  }

  /// Submits parsed Vision AI output. Throws [PhotoRejected] on Path B.
  ///
  /// Passing [supersedesRejectionId] marks this as a reattempt of an earlier
  /// refusal, which the server records so the entry is linked to the rejection
  /// it answered.
  Future<IntakeResult> submitPhoto({
    required String clientEntryId,
    required VisionPayload vision,
    String photoUri = '',
    String? supersedesRejectionId,
  }) async {
    final body = await _send('POST', '/v1/intake/photo', body: {
      'client_entry_id': clientEntryId,
      'photo_uri': photoUri,
      'vision': vision.toJson(),
      if (supersedesRejectionId != null)
        'supersedes_rejection_id': supersedesRejectionId,
    });
    return IntakeResult.fromJson(body);
  }

  Future<Map<String, dynamic>> _send(
    String method,
    String path, {
    Map<String, dynamic>? body,
    bool isRetry = false,
  }) async {
    final token = await tokens.idToken(forceRefresh: isRetry);
    if (token == null || token.isEmpty) {
      throw const Unauthorized();
    }

    final request = http.Request(method, Uri.parse('$baseUrl$path'))
      ..headers['authorization'] = 'Bearer $token'
      ..headers['accept'] = 'application/json';
    if (body != null) {
      request.headers['content-type'] = 'application/json';
      request.body = jsonEncode(body);
    }

    http.Response response;
    try {
      final streamed = await _http.send(request).timeout(timeout);
      response = await http.Response.fromStream(streamed);
    } on TimeoutException {
      throw const NetworkFailure('The server took too long to respond.');
    } on SocketException {
      throw const NetworkFailure();
    } on http.ClientException {
      throw const NetworkFailure();
    }

    // Retry once on an expired token. Bounded by isRetry so a server that
    // always answers 401 cannot put the app in a refresh loop.
    if (response.statusCode == 401 && !isRetry) {
      final code = _decode(response.body)['code'];
      if (code == 'token_expired') {
        return _send(method, path, body: body, isRetry: true);
      }
    }

    if (response.statusCode >= 200 && response.statusCode < 300) {
      return _decode(response.body);
    }
    throw await _mapError(response);
  }

  Map<String, dynamic> _decode(String raw) {
    if (raw.isEmpty) return const {};
    try {
      final decoded = jsonDecode(raw);
      return decoded is Map<String, dynamic> ? decoded : const {};
    } on FormatException {
      return const {};
    }
  }

  Future<ApiException> _mapError(http.Response response) async {
    final body = _decode(response.body);
    final code = body['code'] as String? ?? '';
    final message = body['message'] as String? ?? 'Request failed.';

    switch (code) {
      case 'photo_rejected':
        return PhotoRejected(
          reasoning: (body['validation_reasoning'] as String?)?.trim().isNotEmpty == true
              ? body['validation_reasoning'] as String
              : message,
          rejectionId: body['rejection_id'] as String? ?? '',
          canRetryPhoto: body['can_retry_photo'] != false,
          canEnterManual: body['can_enter_manual'] != false,
          confidence: (body['confidence'] as num?)?.toDouble(),
        );
      case 'profile_not_found':
        return NeedsOnboarding(message);
      case 'token_expired':
        return TokenExpired(message);
      case 'token_revoked':
        // Refreshing cannot recover a revoked session, so drop it here rather
        // than letting the UI retry into a guaranteed failure.
        await tokens.signOut();
        return SessionRevoked(message);
      case 'unauthorized':
        return Unauthorized(message);
      case 'invalid_payload':
        return InvalidRequest(message);
      case 'rejection_already_resolved':
        return RejectionClosed(message);
    }

    // Unknown code: fall back to the status class.
    if (response.statusCode == 401) return Unauthorized(message);
    if (response.statusCode == 404) return NeedsOnboarding(message);
    if (response.statusCode >= 400 && response.statusCode < 500) {
      return InvalidRequest(message);
    }
    return ServerFailure(message, statusCode: response.statusCode);
  }
}
