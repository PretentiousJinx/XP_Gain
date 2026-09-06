package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/PretentiousJinx/xpgain/server/internal/domain"
)

// UpsertUser creates or updates the profile row.
//
// The goals are the denominator of every stat award, so this is a
// security-relevant write even though it looks like settings: lowering the
// calorie goal to 1 would make every meal "on target". Range validation lives
// in domain.Goals.Valid and runs before this is called.
func UpsertUser(ctx context.Context, tx *sql.Tx, userID, timezone string, g domain.Goals, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO users
			(id, timezone, goal_kcal, goal_protein_g, goal_carbs_g, goal_fat_g,
			 created_at, updated_at, is_synced)
		VALUES (?,?,?,?,?,?,?,?,0)
		ON CONFLICT (id) DO UPDATE SET
			timezone       = excluded.timezone,
			goal_kcal      = excluded.goal_kcal,
			goal_protein_g = excluded.goal_protein_g,
			goal_carbs_g   = excluded.goal_carbs_g,
			goal_fat_g     = excluded.goal_fat_g,
			updated_at     = excluded.updated_at,
			is_synced      = 0`,
		userID, timezone, g.KCal, g.ProteinG, g.CarbsG, g.FatG,
		now.Format(rfc3339), now.Format(rfc3339))
	if err != nil {
		return fmt.Errorf("upsert user: %w", err)
	}
	return nil
}

// EnsureCharacter creates the character row if it does not exist.
//
// DO NOTHING on conflict is the point: this runs on every profile update, and
// an upsert that reset the stat columns would wipe a player's progress the
// first time they edited their macro goals.
func EnsureCharacter(ctx context.Context, tx *sql.Tx, userID string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO characters (user_id, level, xp, con_micro, vit_micro, updated_at, is_synced)
		VALUES (?, 1, 0, ?, ?, ?, 0)
		ON CONFLICT (user_id) DO NOTHING`,
		userID, domain.StartingStatMicro, domain.StartingStatMicro, now.Format(rfc3339))
	if err != nil {
		return fmt.Errorf("ensure character: %w", err)
	}
	return nil
}

// EnsureStreak creates the streak row if it does not exist, for the same
// reason EnsureCharacter does not overwrite.
func EnsureStreak(ctx context.Context, tx *sql.Tx, userID string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO streaks
			(user_id, current_streak, longest_streak, last_local_date, last_activity_at, updated_at, is_synced)
		VALUES (?, 0, 0, NULL, NULL, ?, 0)
		ON CONFLICT (user_id) DO NOTHING`,
		userID, now.Format(rfc3339))
	if err != nil {
		return fmt.Errorf("ensure streak: %w", err)
	}
	return nil
}

// UserExists reports whether a profile row is present.
func UserExists(ctx context.Context, tx *sql.Tx, userID string) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ?`, userID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
