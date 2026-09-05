package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/PretentiousJinx/xpgain/server/internal/domain"
)

const rfc3339 = time.RFC3339Nano

// UserProfile is the slice of the user row the intake path needs.
type UserProfile struct {
	ID       string
	Timezone string
	Goals    domain.Goals
}

// LoadUserProfile reads goals and zone. Called inside the write transaction so
// a concurrent goal change cannot land between the read and the stat award.
func LoadUserProfile(ctx context.Context, tx *sql.Tx, userID string) (UserProfile, error) {
	var u UserProfile
	err := tx.QueryRowContext(ctx, `
		SELECT id, timezone, goal_kcal, goal_protein_g, goal_carbs_g, goal_fat_g
		  FROM users WHERE id = ?`, userID).
		Scan(&u.ID, &u.Timezone, &u.Goals.KCal, &u.Goals.ProteinG, &u.Goals.CarbsG, &u.Goals.FatG)
	if errors.Is(err, sql.ErrNoRows) {
		return u, domain.ErrNotFound
	}
	return u, err
}

// FindEntryByClientID backs idempotent replay: if the client retries a submit
// that already succeeded, we return the original row instead of double-logging.
func FindEntryByClientID(ctx context.Context, tx *sql.Tx, userID, clientEntryID string) (string, bool, error) {
	var id string
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM macro_entries WHERE user_id = ? AND client_entry_id = ?`,
		userID, clientEntryID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

// LoadCharacter reads the RPG state.
func LoadCharacter(ctx context.Context, tx *sql.Tx, userID string) (domain.Character, error) {
	var c domain.Character
	err := tx.QueryRowContext(ctx,
		`SELECT user_id, level, xp, con_micro, vit_micro FROM characters WHERE user_id = ?`,
		userID).Scan(&c.UserID, &c.Level, &c.XP, &c.ConMicro, &c.VitMicro)
	if errors.Is(err, sql.ErrNoRows) {
		return c, domain.ErrNotFound
	}
	return c, err
}

// LoadStreak reads streak state; a missing row is a valid zero streak.
func LoadStreak(ctx context.Context, tx *sql.Tx, userID string) (domain.Streak, bool, error) {
	var (
		s        domain.Streak
		lastDate sql.NullString
		lastAct  sql.NullString
	)
	err := tx.QueryRowContext(ctx, `
		SELECT user_id, current_streak, longest_streak, last_local_date, last_activity_at
		  FROM streaks WHERE user_id = ?`, userID).
		Scan(&s.UserID, &s.Current, &s.Longest, &lastDate, &lastAct)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Streak{UserID: userID}, false, nil
	}
	if err != nil {
		return s, false, err
	}
	s.LastLocalDate = lastDate.String
	if lastAct.Valid {
		if t, perr := time.Parse(rfc3339, lastAct.String); perr == nil {
			s.LastActivityAt = &t
		}
	}
	return s, lastDate.Valid && lastDate.String != "", nil
}

// LoadDailyTotals returns the running totals for one local day.
func LoadDailyTotals(ctx context.Context, tx *sql.Tx, userID, localDate string) (domain.Macros, int, error) {
	var (
		m     domain.Macros
		count int
	)
	err := tx.QueryRowContext(ctx, `
		SELECT kcal, protein_g, carbs_g, fat_g, entry_count
		  FROM daily_macro_totals WHERE user_id = ? AND local_date = ?`,
		userID, localDate).Scan(&m.KCal, &m.ProteinG, &m.CarbsG, &m.FatG, &count)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Macros{}, 0, nil
	}
	return m, count, err
}

// InsertEntry writes the food row. is_manual is denormalised alongside source
// so the PvP state-check servers can filter on pedigree with a plain index scan
// rather than parsing the source enum.
func InsertEntry(ctx context.Context, tx *sql.Tx, e domain.MacroEntry, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO macro_entries (
			id, user_id, client_entry_id, local_date,
			kcal, protein_g, carbs_g, fat_g,
			source, is_manual, ai_confidence, ai_model, photo_uri,
			supersedes_rejection_id, logged_at, created_at, updated_at, is_synced
		) VALUES (?,?,?,?, ?,?,?,?, ?,?,?,?,?, ?,?,?,?, 0)`,
		e.ID, e.UserID, e.ClientEntryID, e.LocalDate,
		e.Macros.KCal, e.Macros.ProteinG, e.Macros.CarbsG, e.Macros.FatG,
		string(e.Source), boolToInt(e.IsManual), nullFloat(e.AIConfidence),
		nullStr(e.AIModel), nullStr(e.PhotoURI), nullStr(e.SupersedesID),
		e.LoggedAt.UTC().Format(rfc3339), now.Format(rfc3339), now.Format(rfc3339))
	if err != nil {
		return fmt.Errorf("insert entry: %w", err)
	}
	return nil
}

// UpsertDailyTotals folds the entry into the running total for the local day.
func UpsertDailyTotals(ctx context.Context, tx *sql.Tx, userID, localDate string, after domain.Macros, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO daily_macro_totals
			(user_id, local_date, kcal, protein_g, carbs_g, fat_g, entry_count, updated_at, is_synced)
		VALUES (?,?,?,?,?,?,1,?,0)
		ON CONFLICT (user_id, local_date) DO UPDATE SET
			kcal        = excluded.kcal,
			protein_g   = excluded.protein_g,
			carbs_g     = excluded.carbs_g,
			fat_g       = excluded.fat_g,
			entry_count = daily_macro_totals.entry_count + 1,
			updated_at  = excluded.updated_at,
			is_synced   = 0`,
		userID, localDate, after.KCal, after.ProteinG, after.CarbsG, after.FatG,
		now.Format(rfc3339))
	if err != nil {
		return fmt.Errorf("upsert daily totals: %w", err)
	}
	return nil
}

// UpdateCharacter persists base stats. The stat columns are written as relative
// deltas rather than absolute values, so the statement stays correct even if
// another writer commits between our read and this write. That is belt-and-braces
// on top of the single-writer pool, and it is the property the PvP replay relies on.
func UpdateCharacter(ctx context.Context, tx *sql.Tx, userID string, conDelta, vitDelta, xp, level int, now time.Time) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE characters SET
			con_micro  = MAX(?, con_micro + ?),
			vit_micro  = MAX(?, vit_micro + ?),
			xp         = ?,
			level      = ?,
			updated_at = ?,
			is_synced  = 0
		WHERE user_id = ?`,
		domain.StatFloorMicro, conDelta,
		domain.StatFloorMicro, vitDelta,
		xp, level, now.Format(rfc3339), userID)
	if err != nil {
		return fmt.Errorf("update character: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// UpsertStreak persists streak counters and the activity timestamp.
func UpsertStreak(ctx context.Context, tx *sql.Tx, s domain.Streak, localDate string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO streaks
			(user_id, current_streak, longest_streak, last_local_date, last_activity_at, updated_at, is_synced)
		VALUES (?,?,?,?,?,?,0)
		ON CONFLICT (user_id) DO UPDATE SET
			current_streak   = excluded.current_streak,
			longest_streak   = excluded.longest_streak,
			last_local_date  = excluded.last_local_date,
			last_activity_at = excluded.last_activity_at,
			updated_at       = excluded.updated_at,
			is_synced        = 0`,
		s.UserID, s.Current, s.Longest, localDate,
		now.Format(rfc3339), now.Format(rfc3339))
	if err != nil {
		return fmt.Errorf("upsert streak: %w", err)
	}
	return nil
}

// InsertRejection records Path B. Rejections are persisted, not merely returned:
// the manual-override and reattempt paths reference the rejection id, which is
// what lets the pedigree trail show that a user typed values after the AI declined.
func InsertRejection(ctx context.Context, tx *sql.Tx, r domain.RejectionRecord, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO intake_rejections
			(id, user_id, client_entry_id, validation_reasoning, ai_confidence, ai_model,
			 photo_uri, created_at, updated_at, is_synced)
		VALUES (?,?,?,?,?,?,?,?,?,0)`,
		r.ID, r.UserID, r.ClientEntryID, r.ValidationReasoning,
		nullFloat(r.AIConfidence), nullStr(r.AIModel), nullStr(r.PhotoURI),
		now.Format(rfc3339), now.Format(rfc3339))
	if err != nil {
		return fmt.Errorf("insert rejection: %w", err)
	}
	return nil
}

// LoadRejection fetches a rejection owned by the given user.
func LoadRejection(ctx context.Context, tx *sql.Tx, rejectionID, userID string) (domain.RejectionRecord, error) {
	var (
		r        domain.RejectionRecord
		conf     sql.NullFloat64
		model    sql.NullString
		photo    sql.NullString
		resolved sql.NullString
	)
	err := tx.QueryRowContext(ctx, `
		SELECT id, user_id, client_entry_id, validation_reasoning,
		       ai_confidence, ai_model, photo_uri, resolved_by_entry_id
		  FROM intake_rejections WHERE id = ? AND user_id = ?`,
		rejectionID, userID).
		Scan(&r.ID, &r.UserID, &r.ClientEntryID, &r.ValidationReasoning,
			&conf, &model, &photo, &resolved)
	if errors.Is(err, sql.ErrNoRows) {
		return r, domain.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if conf.Valid {
		r.AIConfidence = &conf.Float64
	}
	r.AIModel = model.String
	r.PhotoURI = photo.String
	r.ResolvedByEntryID = resolved.String
	return r, nil
}

// ResolveRejection links a rejection to the entry that finally satisfied it.
// It refuses to re-resolve, so a replayed request cannot rewrite history.
func ResolveRejection(ctx context.Context, tx *sql.Tx, rejectionID, entryID, userID string, now time.Time) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE intake_rejections
		   SET resolved_by_entry_id = ?, updated_at = ?, is_synced = 0
		 WHERE id = ? AND user_id = ? AND resolved_by_entry_id IS NULL`,
		entryID, now.Format(rfc3339), rejectionID, userID)
	if err != nil {
		return fmt.Errorf("resolve rejection: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrRejectionClosed
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullFloat(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}
