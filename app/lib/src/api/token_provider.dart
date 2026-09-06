import 'dart:async';

/// Supplies the Firebase ID token sent as the bearer credential.
///
/// This is an interface rather than a direct `firebase_auth` call so the API
/// client can be tested without Firebase, and so wiring a real project is a
/// one-class change rather than a change threaded through every request.
abstract class TokenProvider {
  /// Returns the current ID token, or null when nobody is signed in.
  ///
  /// [forceRefresh] is passed straight through to the identity SDK. The client
  /// sets it after a 401 so a token that expired mid-flight is renewed exactly
  /// once rather than on every call.
  Future<String?> idToken({bool forceRefresh = false});

  /// Ends the session. Called when the server reports the token was revoked,
  /// since no amount of refreshing will recover it.
  Future<void> signOut();
}

/// A fixed token, for tests and for pointing a dev build at a local server.
class StaticTokenProvider implements TokenProvider {
  StaticTokenProvider(this._token);

  String? _token;
  int refreshCount = 0;

  @override
  Future<String?> idToken({bool forceRefresh = false}) async {
    if (forceRefresh) refreshCount++;
    return _token;
  }

  /// Simulates a sign-in completing, so tests can model a token appearing
  /// mid-run the way Firebase populates currentUser.
  void setToken(String? token) => _token = token;

  @override
  Future<void> signOut() async => _token = null;
}

/// Signed out: every request will be refused before it leaves the device.
class NoTokenProvider implements TokenProvider {
  const NoTokenProvider();

  @override
  Future<String?> idToken({bool forceRefresh = false}) async => null;

  @override
  Future<void> signOut() async {}
}
