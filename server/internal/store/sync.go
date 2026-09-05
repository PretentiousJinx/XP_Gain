package store

import (
	"context"
	"database/sql"
	"fmt"
)

// DirtyRow is one record awaiting the Firebase auto-save sweep.
type DirtyRow struct {
	Table     string         `json:"table"`
	PK        map[string]any `json:"pk"`
	UpdatedAt string         `json:"updated_at"`
}

// syncedTables lists every table carrying is_synced, with its primary key
// columns. Adding a table to the schema without adding it here means its rows
// are written, flagged dirty, and then never swept -- so keep the two together.
var syncedTables = []struct {
	name string
	pk   []string
}{
	{"macro_entries", []string{"id"}},
	{"intake_rejections", []string{"id"}},
	{"characters", []string{"user_id"}},
	{"streaks", []string{"user_id"}},
	{"daily_macro_totals", []string{"user_id", "local_date"}},
	{"users", []string{"id"}},
}

// PendingSync returns rows with is_synced = 0, oldest first, for the sweep to
// ship to Firebase. It reads from the reader pool so a long sweep never blocks
// the intake write path.
func PendingSync(ctx context.Context, db *DB, limitPerTable int) ([]DirtyRow, error) {
	if limitPerTable <= 0 {
		limitPerTable = 500
	}
	var out []DirtyRow

	for _, t := range syncedTables {
		cols := ""
		for _, c := range t.pk {
			cols += c + ", "
		}
		// Table and column names are from the fixed list above, never from
		// user input, so this interpolation cannot be injected into.
		q := fmt.Sprintf(
			`SELECT %supdated_at FROM %s WHERE is_synced = 0 ORDER BY updated_at LIMIT ?`,
			cols, t.name)

		rows, err := db.Reader().QueryContext(ctx, q, limitPerTable)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", t.name, err)
		}

		for rows.Next() {
			vals := make([]any, len(t.pk)+1)
			ptrs := make([]any, len(vals))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return nil, err
			}
			pk := make(map[string]any, len(t.pk))
			for i, c := range t.pk {
				pk[c] = vals[i]
			}
			updated, _ := vals[len(vals)-1].(string)
			out = append(out, DirtyRow{Table: t.name, PK: pk, UpdatedAt: updated})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// MarkSynced clears the dirty flag after Firebase has acknowledged the write.
//
// The updated_at guard is the important part: if the row changed again while
// the sweep was in flight, its updated_at has moved on and this UPDATE matches
// nothing, so the row correctly stays dirty and is picked up by the next pass.
// Clearing the flag unconditionally would drop that later edit on the floor.
func MarkSynced(ctx context.Context, tx *sql.Tx, table string, pk map[string]any, seenUpdatedAt string) error {
	if !isSyncedTable(table) {
		return fmt.Errorf("mark synced: unknown table %q", table)
	}

	where := "updated_at = ?"
	args := []any{seenUpdatedAt}
	for col, v := range pk {
		where += " AND " + col + " = ?"
		args = append(args, v)
	}

	q := fmt.Sprintf(`UPDATE %s SET is_synced = 1 WHERE %s`, table, where)
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("mark synced %s: %w", table, err)
	}
	return nil
}

func isSyncedTable(name string) bool {
	for _, t := range syncedTables {
		if t.name == name {
			return true
		}
	}
	return false
}

// SweepOnce ships one batch. Wire this to a ticker in cmd/api once the Firebase
// client exists; push is the only part left to implement.
func SweepOnce(ctx context.Context, db *DB, push func(context.Context, []DirtyRow) error) (int, error) {
	rows, err := PendingSync(ctx, db, 500)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	if err := push(ctx, rows); err != nil {
		return 0, fmt.Errorf("push to firebase: %w", err)
	}

	err = db.WithTx(ctx, func(tx *sql.Tx) error {
		for _, r := range rows {
			if err := MarkSynced(ctx, tx, r.Table, r.PK, r.UpdatedAt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(rows), nil
}
