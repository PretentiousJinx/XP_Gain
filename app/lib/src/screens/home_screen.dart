import 'package:flutter/material.dart';

import '../api/models.dart';
import '../model/character_state.dart';
import '../render/sprite_sheet.dart';
import '../state/app_controller.dart';
import '../widgets/avatar_view.dart';

/// The main screen: the avatar, today's macros, and the two ways to log food.
class HomeScreen extends StatelessWidget {
  const HomeScreen({
    super.key,
    required this.controller,
    required this.atlas,
    required this.avatar,
  });

  final AppController controller;
  final SpriteAtlas? atlas;
  final CharacterState avatar;

  @override
  Widget build(BuildContext context) {
    final profile = controller.profile;
    if (profile == null) {
      return const Scaffold(body: Center(child: CircularProgressIndicator()));
    }

    return Scaffold(
      appBar: AppBar(
        title: Text('Level ${profile.character.level}'),
        actions: [
          Padding(
            padding: const EdgeInsets.only(right: 12),
            child: Center(
              child: Text('${profile.streak.current} day streak',
                  key: const Key('streak_label')),
            ),
          ),
          IconButton(
            icon: const Icon(Icons.logout),
            tooltip: 'Sign out',
            onPressed: controller.signOut,
          ),
        ],
      ),
      body: Column(
        children: [
          Expanded(
            flex: 3,
            child: atlas == null
                ? const Center(child: CircularProgressIndicator())
                : AvatarView(
                    state: avatar.copyWith(
                      level: profile.character.level,
                      con: profile.character.con,
                      vit: profile.character.vit,
                    ),
                    atlas: atlas!,
                  ),
          ),
          _StatBar(character: profile.character),
          Expanded(
            flex: 2,
            child: ListView(
              padding: const EdgeInsets.symmetric(horizontal: 16),
              children: [
                _MacroRow(
                  label: 'Calories',
                  consumed: profile.dayTotals.kcal,
                  goal: profile.goals.kcal,
                  unit: 'kcal',
                ),
                _MacroRow(
                  label: 'Protein',
                  consumed: profile.dayTotals.proteinG,
                  goal: profile.goals.proteinG,
                  unit: 'g',
                ),
                _MacroRow(
                  label: 'Carbs',
                  consumed: profile.dayTotals.carbsG,
                  goal: profile.goals.carbsG,
                  unit: 'g',
                ),
                _MacroRow(
                  label: 'Fat',
                  consumed: profile.dayTotals.fatG,
                  goal: profile.goals.fatG,
                  unit: 'g',
                ),
              ],
            ),
          ),
          if (controller.errorMessage != null)
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
              child: Text(
                controller.errorMessage!,
                key: const Key('home_error'),
                style: TextStyle(color: Theme.of(context).colorScheme.error),
              ),
            ),
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 0, 16, 20),
            child: Row(
              children: [
                Expanded(
                  child: OutlinedButton.icon(
                    key: const Key('log_manual'),
                    onPressed: controller.busy
                        ? null
                        : () => _openManualEntry(context, controller),
                    icon: const Icon(Icons.edit),
                    label: const Text('Enter manually'),
                  ),
                ),
                const SizedBox(width: 12),
                Expanded(
                  child: FilledButton.icon(
                    key: const Key('log_photo'),
                    onPressed: controller.busy ? null : () => _capture(context, controller),
                    icon: const Icon(Icons.photo_camera),
                    label: const Text('Photo'),
                  ),
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

/// Shows the Vision AI's own reasoning and the two ways forward.
///
/// The reasoning is rendered verbatim rather than replaced with a generic
/// message: it is the only thing that tells the user *why* the photo failed and
/// therefore what to do differently.
Future<void> showRejectionSheet(BuildContext context, AppController controller) async {
  final rejection = controller.pendingRejection;
  if (rejection == null) return;

  await showModalBottomSheet<void>(
    context: context,
    builder: (sheetContext) => Padding(
      padding: const EdgeInsets.all(20),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text('That photo was not accepted',
              style: Theme.of(sheetContext).textTheme.titleMedium),
          const SizedBox(height: 12),
          Text(rejection.reasoning, key: const Key('rejection_reasoning')),
          const SizedBox(height: 20),
          Row(
            children: [
              if (rejection.canEnterManual)
                Expanded(
                  child: OutlinedButton(
                    key: const Key('rejection_manual'),
                    onPressed: () {
                      Navigator.of(sheetContext).pop();
                      _openManualEntry(context, controller);
                    },
                    child: const Text('Enter manually'),
                  ),
                ),
              if (rejection.canEnterManual && rejection.canRetryPhoto)
                const SizedBox(width: 12),
              if (rejection.canRetryPhoto)
                Expanded(
                  child: FilledButton(
                    key: const Key('rejection_retake'),
                    onPressed: () {
                      Navigator.of(sheetContext).pop();
                      _capture(context, controller);
                    },
                    child: const Text('Retake'),
                  ),
                ),
            ],
          ),
          TextButton(
            onPressed: () {
              controller.clearRejection();
              Navigator.of(sheetContext).pop();
            },
            child: const Text('Cancel'),
          ),
        ],
      ),
    ),
  );
}

/// PLACEHOLDER: stands in for camera capture plus the Vision AI call.
///
/// The server owns the accept/reject decision, so wiring a real camera and
/// vision endpoint changes only what fills this VisionPayload -- no logic below
/// it moves.
Future<void> _capture(BuildContext context, AppController controller) async {
  await controller.logPhoto(const VisionPayload(
    isValidFood: true,
    kcal: 640,
    proteinG: 44,
    carbsG: 58,
    fatG: 21,
    confidence: 0.91,
    model: 'stub-vision',
  ));
  if (context.mounted && controller.pendingRejection != null) {
    await showRejectionSheet(context, controller);
  }
}

Future<void> _openManualEntry(BuildContext context, AppController controller) async {
  final macros = await showDialog<Macros>(
    context: context,
    builder: (_) => const _ManualEntryDialog(),
  );
  if (macros != null) await controller.logManual(macros);
}

class _ManualEntryDialog extends StatefulWidget {
  const _ManualEntryDialog();

  @override
  State<_ManualEntryDialog> createState() => _ManualEntryDialogState();
}

class _ManualEntryDialogState extends State<_ManualEntryDialog> {
  final _kcal = TextEditingController();
  final _protein = TextEditingController();
  final _carbs = TextEditingController();
  final _fat = TextEditingController();

  @override
  void dispose() {
    _kcal.dispose();
    _protein.dispose();
    _carbs.dispose();
    _fat.dispose();
    super.dispose();
  }

  int _v(TextEditingController c) => int.tryParse(c.text.trim()) ?? 0;

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      title: const Text('Log a meal'),
      content: SingleChildScrollView(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            TextField(
              key: const Key('manual_kcal'),
              controller: _kcal,
              keyboardType: TextInputType.number,
              decoration: const InputDecoration(labelText: 'Calories'),
            ),
            TextField(
              key: const Key('manual_protein'),
              controller: _protein,
              keyboardType: TextInputType.number,
              decoration: const InputDecoration(labelText: 'Protein (g)'),
            ),
            TextField(
              key: const Key('manual_carbs'),
              controller: _carbs,
              keyboardType: TextInputType.number,
              decoration: const InputDecoration(labelText: 'Carbs (g)'),
            ),
            TextField(
              key: const Key('manual_fat'),
              controller: _fat,
              keyboardType: TextInputType.number,
              decoration: const InputDecoration(labelText: 'Fat (g)'),
            ),
          ],
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.of(context).pop(),
          child: const Text('Cancel'),
        ),
        FilledButton(
          key: const Key('manual_save'),
          onPressed: () => Navigator.of(context).pop(Macros(
            kcal: _v(_kcal),
            proteinG: _v(_protein),
            carbsG: _v(_carbs),
            fatG: _v(_fat),
          )),
          child: const Text('Log it'),
        ),
      ],
    );
  }
}

class _StatBar extends StatelessWidget {
  const _StatBar({required this.character});

  final CharacterView character;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
      child: Column(
        children: [
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceEvenly,
            children: [
              Text('CON ${character.con}', key: const Key('con_label')),
              Text('VIT ${character.vit}', key: const Key('vit_label')),
              Text('XP ${character.xp}/${character.xpToNext}'),
            ],
          ),
          const SizedBox(height: 6),
          LinearProgressIndicator(value: character.xpFraction),
        ],
      ),
    );
  }
}

class _MacroRow extends StatelessWidget {
  const _MacroRow({
    required this.label,
    required this.consumed,
    required this.goal,
    required this.unit,
  });

  final String label;
  final int consumed;
  final int goal;
  final String unit;

  @override
  Widget build(BuildContext context) {
    final fraction = goal <= 0 ? 0.0 : (consumed / goal).clamp(0.0, 1.0);
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: [
              Text(label),
              Text('$consumed / $goal $unit'),
            ],
          ),
          const SizedBox(height: 4),
          LinearProgressIndicator(value: fraction),
        ],
      ),
    );
  }
}
