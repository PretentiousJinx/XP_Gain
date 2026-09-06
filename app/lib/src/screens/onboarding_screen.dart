import 'package:flutter/material.dart';

import '../api/models.dart';
import '../state/app_controller.dart';

/// First-run setup: pick a timezone and daily macro targets.
///
/// The server range-checks these and returns a message naming the offending
/// field, so this form does not duplicate the rules -- it shows what came back.
/// Duplicating them would guarantee the two drift apart.
class OnboardingScreen extends StatefulWidget {
  const OnboardingScreen({super.key, required this.controller});

  final AppController controller;

  @override
  State<OnboardingScreen> createState() => _OnboardingScreenState();
}

/// A short list beats a dependency on a full tz database for a first release.
/// The server accepts any IANA name and rejects the rest.
const _commonZones = <String>[
  'UTC',
  'America/Los_Angeles',
  'America/Denver',
  'America/Chicago',
  'America/New_York',
  'Europe/London',
  'Europe/Berlin',
  'Europe/Madrid',
  'Asia/Kolkata',
  'Asia/Singapore',
  'Asia/Tokyo',
  'Australia/Sydney',
];

class _OnboardingScreenState extends State<OnboardingScreen> {
  final _formKey = GlobalKey<FormState>();
  String _zone = 'UTC';
  late final _kcal = TextEditingController(text: '${Goals.defaults.kcal}');
  late final _protein = TextEditingController(text: '${Goals.defaults.proteinG}');
  late final _carbs = TextEditingController(text: '${Goals.defaults.carbsG}');
  late final _fat = TextEditingController(text: '${Goals.defaults.fatG}');

  @override
  void dispose() {
    _kcal.dispose();
    _protein.dispose();
    _carbs.dispose();
    _fat.dispose();
    super.dispose();
  }

  Future<void> _submit() async {
    if (!(_formKey.currentState?.validate() ?? false)) return;
    await widget.controller.saveProfile(
      timezone: _zone,
      goals: Goals(
        kcal: int.parse(_kcal.text),
        proteinG: int.parse(_protein.text),
        carbsG: int.parse(_carbs.text),
        fatG: int.parse(_fat.text),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final c = widget.controller;

    return Scaffold(
      appBar: AppBar(title: const Text('Set your daily targets')),
      body: Form(
        key: _formKey,
        child: ListView(
          padding: const EdgeInsets.all(20),
          children: [
            Text(
              'Your character gains Constitution and Vitality as you hit these '
              'targets, so they are worth getting roughly right.',
              style: Theme.of(context).textTheme.bodyMedium,
            ),
            const SizedBox(height: 24),
            DropdownButtonFormField<String>(
              key: const Key('timezone'),
              initialValue: _zone,
              decoration: const InputDecoration(
                labelText: 'Time zone',
                helperText: 'Streaks advance on your local calendar day.',
              ),
              items: [
                for (final z in _commonZones)
                  DropdownMenuItem(value: z, child: Text(z)),
              ],
              onChanged: (v) => setState(() => _zone = v ?? 'UTC'),
            ),
            const SizedBox(height: 8),
            _NumberField(key: const Key('goal_kcal'), controller: _kcal, label: 'Daily calories'),
            _NumberField(key: const Key('goal_protein'), controller: _protein, label: 'Protein (g)'),
            _NumberField(key: const Key('goal_carbs'), controller: _carbs, label: 'Carbs (g)'),
            _NumberField(key: const Key('goal_fat'), controller: _fat, label: 'Fat (g)'),
            const SizedBox(height: 20),
            if (c.errorMessage != null)
              Padding(
                padding: const EdgeInsets.only(bottom: 12),
                child: Text(
                  c.errorMessage!,
                  key: const Key('onboarding_error'),
                  style: TextStyle(color: Theme.of(context).colorScheme.error),
                ),
              ),
            FilledButton(
              key: const Key('save_goals'),
              onPressed: c.busy ? null : _submit,
              child: c.busy
                  ? const SizedBox(
                      height: 18, width: 18, child: CircularProgressIndicator(strokeWidth: 2))
                  : const Text('Start playing'),
            ),
          ],
        ),
      ),
    );
  }
}

class _NumberField extends StatelessWidget {
  const _NumberField({super.key, required this.controller, required this.label});

  final TextEditingController controller;
  final String label;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 6),
      child: TextFormField(
        controller: controller,
        keyboardType: TextInputType.number,
        decoration: InputDecoration(labelText: label),
        validator: (v) {
          final n = int.tryParse(v?.trim() ?? '');
          if (n == null || n <= 0) return 'Enter a whole number above zero';
          return null;
        },
      ),
    );
  }
}
