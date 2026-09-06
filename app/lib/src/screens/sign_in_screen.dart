import 'package:flutter/material.dart';

import '../auth/auth_service.dart';

/// Email and password, with the SMS challenge folded into the same screen.
///
/// The challenge is a step here rather than a separate route because it is the
/// same logical act: the user is still signing in, and Firebase is holding a
/// half-finished attempt. Pushing a route would let them navigate back and
/// strand that attempt.
class SignInScreen extends StatefulWidget {
  const SignInScreen({
    super.key,
    required this.gateway,
    required this.onSignedIn,
    this.message,
  });

  final SignInGateway gateway;

  /// Called once Firebase reports a complete session. The server still decides
  /// whether that session is good enough -- it may yet demand enrolment.
  final Future<void> Function() onSignedIn;

  /// A message carried over from a previous session, e.g. "you were signed out".
  final String? message;

  @override
  State<SignInScreen> createState() => _SignInScreenState();
}

enum _Mode { signIn, register }

class _SignInScreenState extends State<SignInScreen> {
  final _formKey = GlobalKey<FormState>();
  final _email = TextEditingController();
  final _password = TextEditingController();
  final _code = TextEditingController();

  _Mode _mode = _Mode.signIn;
  bool _busy = false;
  String? _error;
  String? _notice;

  // Set once the password is accepted but a second factor is outstanding.
  Object? _resolver;
  String? _verificationId;

  bool get _awaitingCode => _resolver != null;

  @override
  void dispose() {
    _email.dispose();
    _password.dispose();
    _code.dispose();
    super.dispose();
  }

  void _set(void Function() f) {
    if (mounted) setState(f);
  }

  Future<void> _submit() async {
    if (!(_formKey.currentState?.validate() ?? false)) return;
    _set(() {
      _busy = true;
      _error = null;
      _notice = null;
    });

    final email = _email.text.trim();
    final step = _mode == _Mode.signIn
        ? await widget.gateway.signIn(email: email, password: _password.text)
        : await widget.gateway.register(email: email, password: _password.text);

    switch (step.outcome) {
      case AuthOutcome.failed:
        _set(() {
          _busy = false;
          _error = step.message;
        });

      case AuthOutcome.needsSmsCode:
        // The password was right. Ask Firebase to text the code.
        final sent = await widget.gateway.sendSignInCode(step.resolver!);
        _set(() {
          _busy = false;
          if (sent.outcome == AuthOutcome.failed) {
            _error = sent.message;
          } else {
            _resolver = step.resolver;
            _verificationId = sent.verificationId;
            _notice = 'We texted you a code.';
          }
        });

      case AuthOutcome.signedIn:
      case AuthOutcome.needsEnrollment:
        // Both mean Firebase has a session. Whether it satisfies the server is
        // the server's call, so hand off and let the app re-bootstrap.
        _set(() => _busy = false);
        await widget.onSignedIn();
    }
  }

  Future<void> _submitCode() async {
    _set(() {
      _busy = true;
      _error = null;
    });

    final step = await widget.gateway.submitSignInCode(
      resolver: _resolver!,
      verificationId: _verificationId ?? '',
      smsCode: _code.text.trim(),
    );

    if (step.outcome == AuthOutcome.failed) {
      _set(() {
        _busy = false;
        _error = step.message;
      });
      return;
    }
    _set(() => _busy = false);
    await widget.onSignedIn();
  }

  Future<void> _resetPassword() async {
    final email = _email.text.trim();
    if (email.isEmpty) {
      _set(() => _error = 'Enter your email address first.');
      return;
    }
    _set(() {
      _busy = true;
      _error = null;
    });
    await widget.gateway.sendPasswordReset(email);
    // Always reports success. Saying whether the address exists would let
    // anyone test which emails have accounts.
    _set(() {
      _busy = false;
      _notice = 'If that address has an account, a reset link is on its way.';
    });
  }

  @override
  Widget build(BuildContext context) {
    final registering = _mode == _Mode.register;

    return Scaffold(
      body: SafeArea(
        child: Form(
          key: _formKey,
          child: ListView(
            padding: const EdgeInsets.all(24),
            children: [
              const SizedBox(height: 32),
              Text('XP_Gain', style: Theme.of(context).textTheme.headlineMedium),
              const SizedBox(height: 8),
              Text(
                _awaitingCode
                    ? 'Enter the code we texted you.'
                    : 'Log your meals, level up your character.',
                style: Theme.of(context).textTheme.bodyMedium,
              ),
              const SizedBox(height: 32),

              if (!_awaitingCode) ...[
                TextFormField(
                  key: const Key('signin_email'),
                  controller: _email,
                  enabled: !_busy,
                  keyboardType: TextInputType.emailAddress,
                  autocorrect: false,
                  autofillHints: const [AutofillHints.email],
                  decoration: const InputDecoration(labelText: 'Email'),
                  validator: (v) {
                    final t = v?.trim() ?? '';
                    if (t.isEmpty) return 'Enter your email address';
                    if (!t.contains('@') || !t.contains('.')) {
                      return 'That does not look like an email address';
                    }
                    return null;
                  },
                ),
                const SizedBox(height: 12),
                TextFormField(
                  key: const Key('signin_password'),
                  controller: _password,
                  enabled: !_busy,
                  obscureText: true,
                  autofillHints: [
                    registering ? AutofillHints.newPassword : AutofillHints.password
                  ],
                  decoration: const InputDecoration(labelText: 'Password'),
                  validator: (v) {
                    final t = v ?? '';
                    if (t.isEmpty) return 'Enter your password';
                    // Only enforced when creating an account; an existing
                    // password predates whatever rule we pick today.
                    if (registering && t.length < 8) {
                      return 'Use at least 8 characters';
                    }
                    return null;
                  },
                ),
              ] else
                TextFormField(
                  key: const Key('signin_code'),
                  controller: _code,
                  enabled: !_busy,
                  keyboardType: TextInputType.number,
                  autofillHints: const [AutofillHints.oneTimeCode],
                  decoration: const InputDecoration(labelText: 'Six-digit code'),
                ),

              if (_error != null) ...[
                const SizedBox(height: 16),
                Text(_error!,
                    key: const Key('signin_error'),
                    style: TextStyle(color: Theme.of(context).colorScheme.error)),
              ],
              if (_notice != null) ...[
                const SizedBox(height: 16),
                Text(_notice!, key: const Key('signin_notice')),
              ],
              if (widget.message != null && _error == null && _notice == null) ...[
                const SizedBox(height: 16),
                Text(widget.message!, key: const Key('signin_carried_message')),
              ],

              const SizedBox(height: 24),
              FilledButton(
                key: const Key('signin_submit'),
                onPressed: _busy ? null : (_awaitingCode ? _submitCode : _submit),
                child: _busy
                    ? const SizedBox(
                        height: 18,
                        width: 18,
                        child: CircularProgressIndicator(strokeWidth: 2))
                    : Text(_awaitingCode
                        ? 'Verify'
                        : (registering ? 'Create account' : 'Sign in')),
              ),

              if (_awaitingCode)
                TextButton(
                  key: const Key('signin_cancel_code'),
                  onPressed: _busy
                      ? null
                      : () => _set(() {
                            _resolver = null;
                            _verificationId = null;
                            _code.clear();
                            _error = null;
                            _notice = null;
                          }),
                  child: const Text('Use a different account'),
                )
              else ...[
                TextButton(
                  key: const Key('signin_toggle_mode'),
                  onPressed: _busy
                      ? null
                      : () => _set(() {
                            _mode = registering ? _Mode.signIn : _Mode.register;
                            _error = null;
                            _notice = null;
                          }),
                  child: Text(registering
                      ? 'I already have an account'
                      : 'Create an account'),
                ),
                if (!registering)
                  TextButton(
                    key: const Key('signin_forgot'),
                    onPressed: _busy ? null : _resetPassword,
                    child: const Text('Forgot password?'),
                  ),
              ],
            ],
          ),
        ),
      ),
    );
  }
}
