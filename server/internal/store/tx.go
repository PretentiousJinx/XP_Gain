package store

import (
	"context"
	"database/sql"
	"fmt"
)

// WithTx runs fn inside a single write transaction on the serialised writer
// pool. It is the only sanctioned way to mutate state.
//
// The rollback is deferred rather than written on each error path, because the
// failure mode it guards against is the one that is easy to miss: an early
// return -- or a panic in game-rule code -- that leaves the writer connection
// holding a transaction. Since the writer pool has exactly one connection, a
// single leaked transaction deadlocks every subsequent write for the life of
// the process. The panic is re-raised after cleanup so it still surfaces.
func (db *DB) WithTx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := db.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
			return
		}
		if cErr := tx.Commit(); cErr != nil {
			err = fmt.Errorf("commit: %w", cErr)
		}
	}()

	err = fn(tx)
	return err
}
