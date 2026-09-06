import 'package:flutter/material.dart';

import '../auth/auth_service.dart';

/// Adds a phone second factor to a signed-in account.
///
/// Reached when the server answers 403 `mfa_required`: the password was
/// correct and the session is real, but it carries no
/// `firebase.sign_in_second_factor` claim, so the API will not serve it.
///
/// The session is deliberately kept rather than signed out — enrolling requires
/// being signed in, so dropping it here would strand the user in a loop where
/// the only way to satisfy the server is a step they can no longer take.
class MfaEnrollScreen extends StatefulWidget {
  const MfaEnrollScreen({
    super.key,
    required this.enroller,
    required this.onEnrolled,
    this.onSignOut,
    this.message,
  });

  final MfaEnroller enroller;

  /// Called once the factor is enrolled and a fresh token has been minted.
  final Future<void> Function() onEnrolled;

  final Future<void> Function()? onSignOut;
  final String? message;

  @override
  State<MfaEnrollScreen> createState() => _MfaEnrollScreenState();
}

class _MfaEnrollScreenState extends State<MfaEnrollScreen> {
  final _phone = TextEditingController();
  final _code = TextEditingController();

  String? _verificationId;
  String? _error;
  bool _busy = false;

  @override
  void dispose() {
    _phone.dispose();
    _code.dispose();
    super.dispose();
  }

  Future<void> _sendCode() async {
    final number = _phone.text.trim();
    if (number.isEmpty) {
      setState(() => _error = 'Enter your phone number, including the country code.');
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
    });

    final step = await widget.enroller.startEnrollment(number);
    if (!mounted) return;
    setState(() {
      _busy = false;
      if (step.outcome == AuthOutcome.failed) {
        _error = step.message;
      } else {
        _verificationId = step.verificationId;
      }
    });
  }

  Future<void> _confirm() async {
    final id = _verificationId;
    if (id == null) return;
    setState(() {
      _busy = true;
      _error = null;
    });

    final step = await widget.enroller.confirmEnrollment(
      verificationId: id,
      smsCode: _code.text.trim(),
    );
    if (!mounted) return;

    if (step.outcome == AuthOutcome.failed) {
      setState(() {
        _busy = false;
        _error = step.message;
      });
      return;
    }

    // AuthService already forced a token refresh, so the next request carries
    // the second-factor claim. Without that the server would keep answering
    // 403 with a credential that is now stale rather than wrong.
    await widget.onEnrolled();
    if (mounted) setState(() => _busy = false);
  }

  @override
  Widget build(BuildContext context) {
    final awaitingCode = _verificationId != null;

    return Scaffold(
      appBar: AppBar(
        title: const Text('Secure your account'),
        actions: [
          if (widget.onSignOut != null)
            TextButton(
              key: const Key('mfa_sign_out'),
              onPressed: _busy ? null : () => widget.onSignOut!(),
              child: const Text('Sign out'),
            ),
        ],
      ),
      body: ListView(
        padding: const EdgeInsets.all(20),
        children: [
          Text(
            widget.message ??
                'This account needs two-factor authentication before you can log food.',
            style: Theme.of(context).textTheme.bodyMedium,
          ),
          const SizedBox(height: 8),
          Text(
            'We will text you a code. Face ID or a fingerprint can unlock the app '
            'afterwards, but it cannot replace this step.',
            style: Theme.of(context).textTheme.bodySmall,
          ),
          const SizedBox(height: 24),
          TextField(
            key: const Key('mfa_phone'),
            controller: _phone,
            enabled: !awaitingCode && !_busy,
            keyboardType: TextInputType.phone,
            decoration: const InputDecoration(
              labelText: 'Phone number',
              hintText: '+1 555 010 0000',
            ),
          ),
          if (awaitingCode) ...[
            const SizedBox(height: 16),
            TextField(
              key: const Key('mfa_code'),
              controller: _code,
              enabled: !_busy,
              keyboardType: TextInputType.number,
              decoration: const InputDecoration(labelText: 'Six-digit code'),
            ),
          ],
          if (_error != null) ...[
            const SizedBox(height: 16),
            Text(_error!,
                key: const Key('mfa_error'),
                style: TextStyle(color: Theme.of(context).colorScheme.error)),
          ],
          const SizedBox(height: 24),
          FilledButton(
            key: const Key('mfa_submit'),
            onPressed: _busy ? null : (awaitingCode ? _confirm : _sendCode),
            child: _busy
                ? const SizedBox(
                    height: 18, width: 18, child: CircularProgressIndicator(strokeWidth: 2))
                : Text(awaitingCode ? 'Confirm code' : 'Send code'),
          ),
          if (awaitingCode)
            TextButton(
              key: const Key('mfa_change_number'),
              onPressed: _busy
                  ? null
                  : () => setState(() {
                        _verificationId = null;
                        _code.clear();
                        _error = null;
                      }),
              child: const Text('Use a different number'),
            ),
        ],
      ),
    );
  }
}
