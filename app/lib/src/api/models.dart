import 'package:flutter/foundation.dart';

/// Wire models mirroring the Go service's JSON.
///
/// Every field is parsed defensively: the server is trusted, but a version skew
/// between a deployed API and an installed app is normal and must degrade to a
/// missing value rather than a crash on launch.

int _int(Object? v) => switch (v) {
      int i => i,
      double d => d.round(),
      String s => int.tryParse(s) ?? 0,
      _ => 0,
    };

String _str(Object? v) => v is String ? v : '';

@immutable
class Macros {
  const Macros({this.kcal = 0, this.proteinG = 0, this.carbsG = 0, this.fatG = 0});

  final int kcal;
  final int proteinG;
  final int carbsG;
  final int fatG;

  factory Macros.fromJson(Map<String, dynamic>? j) {
    if (j == null) return const Macros();
    return Macros(
      kcal: _int(j['kcal']),
      proteinG: _int(j['protein_g']),
      carbsG: _int(j['carbs_g']),
      fatG: _int(j['fat_g']),
    );
  }

  Map<String, dynamic> toJson() => {
        'kcal': kcal,
        'protein_g': proteinG,
        'carbs_g': carbsG,
        'fat_g': fatG,
      };

  @override
  bool operator ==(Object other) =>
      other is Macros &&
      other.kcal == kcal &&
      other.proteinG == proteinG &&
      other.carbsG == carbsG &&
      other.fatG == fatG;

  @override
  int get hashCode => Object.hash(kcal, proteinG, carbsG, fatG);
}

/// Daily targets. The JSON keys carry the `goal_` prefix the server uses.
@immutable
class Goals {
  const Goals({
    required this.kcal,
    required this.proteinG,
    required this.carbsG,
    required this.fatG,
  });

  final int kcal;
  final int proteinG;
  final int carbsG;
  final int fatG;

  static const Goals defaults =
      Goals(kcal: 2200, proteinG: 160, carbsG: 220, fatG: 70);

  factory Goals.fromJson(Map<String, dynamic>? j) {
    if (j == null) return defaults;
    return Goals(
      kcal: _int(j['goal_kcal']),
      proteinG: _int(j['goal_protein_g']),
      carbsG: _int(j['goal_carbs_g']),
      fatG: _int(j['goal_fat_g']),
    );
  }

  Map<String, dynamic> toJson() => {
        'goal_kcal': kcal,
        'goal_protein_g': proteinG,
        'goal_carbs_g': carbsG,
        'goal_fat_g': fatG,
      };
}

@immutable
class CharacterView {
  const CharacterView({
    this.level = 1,
    this.xp = 0,
    this.xpToNext = 100,
    this.con = 5,
    this.vit = 5,
  });

  final int level;
  final int xp;
  final int xpToNext;
  final int con;
  final int vit;

  double get xpFraction => xpToNext <= 0 ? 0 : (xp / xpToNext).clamp(0.0, 1.0);

  factory CharacterView.fromJson(Map<String, dynamic>? j) {
    if (j == null) return const CharacterView();
    return CharacterView(
      level: _int(j['level']),
      xp: _int(j['xp']),
      xpToNext: _int(j['xp_to_next']),
      con: _int(j['con']),
      vit: _int(j['vit']),
    );
  }
}

@immutable
class Streak {
  const Streak({this.current = 0, this.longest = 0, this.lastLocalDate = ''});

  final int current;
  final int longest;
  final String lastLocalDate;

  factory Streak.fromJson(Map<String, dynamic>? j) {
    if (j == null) return const Streak();
    return Streak(
      current: _int(j['current_streak']),
      longest: _int(j['longest_streak']),
      lastLocalDate: _str(j['last_local_date']),
    );
  }
}

/// The account view returned by GET and PUT /v1/me.
@immutable
class Profile {
  const Profile({
    required this.userId,
    required this.timezone,
    required this.goals,
    required this.localDate,
    required this.dayTotals,
    required this.remaining,
    required this.character,
    required this.streak,
    this.created = false,
  });

  final String userId;
  final String timezone;
  final Goals goals;
  final String localDate;
  final Macros dayTotals;
  final Macros remaining;
  final CharacterView character;
  final Streak streak;
  final bool created;

  factory Profile.fromJson(Map<String, dynamic> j) => Profile(
        userId: _str(j['user_id']),
        timezone: _str(j['timezone']),
        goals: Goals.fromJson(j['goals'] as Map<String, dynamic>?),
        localDate: _str(j['local_date']),
        dayTotals: Macros.fromJson(j['day_totals'] as Map<String, dynamic>?),
        remaining: Macros.fromJson(j['remaining'] as Map<String, dynamic>?),
        character: CharacterView.fromJson(j['character'] as Map<String, dynamic>?),
        streak: Streak.fromJson(j['streak'] as Map<String, dynamic>?),
        created: j['created'] == true,
      );
}

/// The result of an accepted intake, from either fallback path.
@immutable
class IntakeResult {
  const IntakeResult({
    required this.entryId,
    required this.source,
    required this.isManual,
    required this.dayTotals,
    required this.remaining,
    required this.character,
    required this.streak,
    required this.xpAwarded,
    required this.levelsGained,
    this.replayed = false,
  });

  final String entryId;
  final String source;
  final bool isManual;
  final Macros dayTotals;
  final Macros remaining;
  final CharacterView character;
  final Streak streak;
  final int xpAwarded;
  final int levelsGained;

  /// True when the server recognised this as a retry of a submission it had
  /// already applied, so the UI can avoid replaying the level-up animation.
  final bool replayed;

  factory IntakeResult.fromJson(Map<String, dynamic> j) => IntakeResult(
        entryId: _str(j['entry_id']),
        source: _str(j['source']),
        isManual: j['is_manual'] == true,
        dayTotals: Macros.fromJson(j['day_totals'] as Map<String, dynamic>?),
        remaining: Macros.fromJson(j['remaining'] as Map<String, dynamic>?),
        character: CharacterView.fromJson(j['character'] as Map<String, dynamic>?),
        streak: Streak.fromJson(j['streak'] as Map<String, dynamic>?),
        xpAwarded: _int(j['xp_awarded']),
        levelsGained: _int(j['levels_gained']),
        replayed: j['replayed'] == true,
      );
}

/// Parsed Vision AI output, forwarded to the server for the routing decision.
///
/// The client does not interpret these fields -- it only carries them. Deciding
/// whether a photo counts is the server's job, because that decision moves base
/// stats and cannot be trusted to the device.
@immutable
class VisionPayload {
  const VisionPayload({
    required this.isValidFood,
    this.validationReasoning = '',
    this.kcal = 0,
    this.proteinG = 0,
    this.carbsG = 0,
    this.fatG = 0,
    this.confidence = 0,
    this.model = '',
  });

  final bool isValidFood;
  final String validationReasoning;
  final int kcal;
  final int proteinG;
  final int carbsG;
  final int fatG;
  final double confidence;
  final String model;

  Map<String, dynamic> toJson() => {
        'is_valid_food': isValidFood,
        'validation_reasoning': validationReasoning,
        'kcal': kcal,
        'protein_g': proteinG,
        'carbs_g': carbsG,
        'fat_g': fatG,
        'confidence': confidence,
        'model': model,
      };
}
