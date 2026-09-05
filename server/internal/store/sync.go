package store

import (
	"context"
	"database/sql"
	"fmt"
)

// DirtyRow is one record awaiting the Firebase auto-save sweep.
//
// Fields carries the full row, not just its key: a sweep that knew only which
// rows were dirty would still have to re-query each one to ship it, turning one
// scan into N round trips against the same database the intake path is writing.
type DirtyRow struct {
	Table     string         `json:"table"`
	PK        map[string]any `json:"pk"`
	OwnerUID  string         `json:"owner_uid"`
	UpdatedAt string         `json:"updated_at"`
	Fields    map[string]any `json:"fields"`
}

// DocID renders the primary key as a single Firestore document ID. Composite
// keys are joined so that (user, date) addresses one deterministic document.
func (r DirtyRow) DocID(pkCols []string) string {
	id := ""
	for i, c := range pkCols {
		if i > 0 {
			id += "_"
		}
		id += fmt.Sprint(r.PK[c])
	}
	return id
}

type syncedTable struct {
	name string
	pk   []string
	// owner names the column holding the Firebase UID, so the sweep can file
	// each row under the right user's document subtree.
	owner string
}

// syncedTables lists every table carrying is_synced. Adding a table to the
// schema without adding it here means its rows are written, flagged dirty, and
// then never swept -- so keep the two together.
var syncedTables = []syncedTable{
	{"macro_entries", []string{"id"}, "user_id"},
	{"intake_rejections", []string{"id"}, "user_id"},
	{"characters", []string{"user_id"}, "user_id"},
	{"streaks", []string{"user_id"}, "user_id"},
	{"daily_macro_totals", []string{"user_id", "local_date"}, "user_id"},
	{"users", []string{"id"}, "id"},
}

// SyncedTable returns the descriptor for a table, if it is swept.
func SyncedTable(name string) (pk []string, owner string, ok bool) {
	for _, t := range syncedTables {
		if t.name == name {
			return t.pk, t.owner, true
		}
	}
	return nil, "", false
}

// PendingSync returns whole rows with is_synced = 0, oldest first. It reads from
// the reader pool so a long sweep never blocks the intake write path.
func PendingSync(ctx context.Context, db *DB, limitPerTable int) ([]DirtyRow, error) {
	if limitPerTable <= 0 {
		limitPerTable = 500
	}
	var out []DirtyRow

	for _, t := range syncedTables {
		// Table names come from the fixed list above, never from user input,
		// so this interpolation cannot be injected into.
		q := fmt.Sprintf(
			`SELECT * FROM %s WHERE is_synced = 0 ORDER BY updated_at LIMIT ?`, t.name)

		rows, err := db.Reader().QueryContext(ctx, q, limitPerTable)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", t.name, err)
		}

		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("columns %s: %w", t.name, err)
		}

		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return nil, err
			}

			row := DirtyRow{
				Table:  t.name,
				PK:     make(map[string]any, len(t.pk)),
				Fields: make(map[string]any, len(cols)),
			}
			for i, c := range cols {
				v := normalise(vals[i])
				// is_synced is bookkeeping for this database alone; shipping it
				// would let a restored backup look already-synced.
				if c != "is_synced" {
					row.Fields[c] = v
				}
				if c == t.owner {
					row.OwnerUID = fmt.Sprint(v)
				}
				if c == "updated_at" {
					row.UpdatedAt, _ = v.(string)
				}
				for _, k := range t.pk {
					if c == k {
						row.PK[k] = v
					}
				}
			}
			out = append(out, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// normalise converts driver types into values the encoders can rely on.
// SQLite hands back TEXT as []byte on some paths and string on others; letting
// that inconsistency through would make a column serialise as base64 in one
// sweep and as a string in the next.
func normalise(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

// MarkSynced clears the dirty flag after Firebase has acknowledged the write.
//
// The updated_at guard is the important part: if the row changed again while
// the sweep was in flight, its updated_at has moved on and this UPDATE matches
// nothing, so the row correctly stays dirty and is picked up by the next pass.
// Clearing the flag unconditionally would drop that later edit on the floor.
func MarkSynced(ctx context.Context, tx *sql.Tx, table string, pk map[string]any, seenUpdatedAt string) error {
	if _, _, ok := SyncedTable(table); !ok {
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

// Pusher ships a batch of dirty rows to remote storage.
type Pusher interface {
	Push(ctx context.Context, rows []DirtyRow) error
}

// PusherFunc adapts a function to Pusher.
type PusherFunc func(ctx context.Context, rows []DirtyRow) error

func (f PusherFunc) Push(ctx context.Context, rows []DirtyRow) error { return f(ctx, rows) }

// SweepOnce ships one batch and clears the flags the push acknowledged.
//
// The flags are cleared only after the push returns nil. A failed push leaves
// every row dirty, so the next pass retries it: losing a user's food log to a
// transient network error is not recoverable, while sending a row twice is
// harmless because the remote write is an idempotent overwrite keyed by
// document ID.
func SweepOnce(ctx context.Context, db *DB, push Pusher) (int, error) {
	rows, err := PendingSync(ctx, db, 500)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	if err := push.Push(ctx, rows); err != nil {
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
