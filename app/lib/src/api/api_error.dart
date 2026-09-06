import 'package:flutter/foundation.dart';

/// Errors the API can return, as types the UI can switch on.
///
/// The server sends a machine-readable `code` precisely so the client does not
/// have to pattern-match on prose. Each subclass below corresponds to a code
/// that requires a *different* user-facing response, which is the whole reason
/// they are distinguished rather than collapsed into one message string.
@immutable
sealed class ApiException implements Exception {
  const ApiException(this.message);
  final String message;

  @override
  String toString() => '$runtimeType: $message';
}

/// Path B: the Vision AI declined the photo.
///
/// Not a failure of the app or the network -- an expected answer that happens
/// to be "no". It carries the model's own reasoning to show the user, and the
/// rejection id needed to link a retry or a manual override to this refusal.
@immutable
class PhotoRejected extends ApiException {
  const PhotoRejected({
    required this.reasoning,
    required this.rejectionId,
    this.canRetryPhoto = true,
    this.canEnterManual = true,
    this.confidence,
  }) : super(reasoning);

  final String reasoning;
  final String rejectionId;
  final bool canRetryPhoto;
  final bool canEnterManual;
  final double? confidence;
}

/// The account exists in Firebase but has never been provisioned here.
@immutable
class NeedsOnboarding extends ApiException {
  const NeedsOnboarding([super.message = 'This account has not been set up yet.']);
}

/// The ID token expired. Refreshing and retrying will work.
@immutable
class TokenExpired extends ApiException {
  const TokenExpired([super.message = 'Session expired.']);
}

/// The session was revoked or the account disabled. Refreshing will *not* help;
/// the user has to sign in again.
@immutable
class SessionRevoked extends ApiException {
  const SessionRevoked([super.message = 'Please sign in again.']);
}

/// Not signed in, or the token was rejected outright.
@immutable
class Unauthorized extends ApiException {
  const Unauthorized([super.message = 'Sign in required.']);
}

/// The request was malformed or out of range; the message names the field.
@immutable
class InvalidRequest extends ApiException {
  const InvalidRequest(super.message);
}

/// A rejection someone already answered.
@immutable
class RejectionClosed extends ApiException {
  const RejectionClosed([super.message = 'That photo was already resolved.']);
}

/// The server failed, or the response was unintelligible.
@immutable
class ServerFailure extends ApiException {
  const ServerFailure(super.message, {this.statusCode});
  final int? statusCode;
}

/// The request never reached the server.
///
/// Kept distinct from [ServerFailure] because the user-facing advice differs:
/// this one is worth an automatic retry and a "you appear to be offline"
/// message, rather than "something went wrong".
@immutable
class NetworkFailure extends ApiException {
  const NetworkFailure([super.message = 'Could not reach the server.']);
}
