// Package store owns all SQLite access. Nothing above it may hold a *sql.DB.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"net/url"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no cgo, so no gcc on the build host
)

//go:embed schema.sql
var schemaSQL string

// DB holds two pools over one SQLite file. This split is the core of the
// concurrency design:
//
//	writer - MaxOpenConns(1). SQLite permits exactly one writer at a time. Rather
//	         than letting N goroutines collide and retry on SQLITE_BUSY, we make
//	         the pool itself the queue: contention becomes a bounded wait for a
//	         connection instead of a storm of failed transactions.
//	reader - MaxOpenConns(N). Under WAL, readers never block the writer and the
//	         writer never blocks readers, so reads scale freely.
//
// Both pools are safe for concurrent use by many goroutines; *sql.DB is itself
// a concurrency-safe handle, so no mutex is needed in this package.
type DB struct {
	writer *sql.DB
	reader *sql.DB
	now    func() time.Time
}

// Open prepares the database file and applies the schema.
func Open(path string, maxReaders int) (*DB, error) {
	if maxReaders < 1 {
		maxReaders = 4
	}

	writer, err := open(path, true)
	if err != nil {
		return nil, fmt.Errorf("open writer: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	writer.SetConnMaxLifetime(0) // a single long-lived writer conn keeps WAL hot

	reader, err := open(path, false)
	if err != nil {
		writer.Close()
		return nil, fmt.Errorf("open reader: %w", err)
	}
	reader.SetMaxOpenConns(maxReaders)
	reader.SetMaxIdleConns(maxReaders)

	db := &DB{writer: writer, reader: reader, now: time.Now}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := writer.ExecContext(ctx, schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return db, nil
}

func open(path string, isWriter bool) (*sql.DB, error) {
	q := url.Values{}
	// WAL is what allows concurrent readers alongside the single writer.
	q.Add("_pragma", "journal_mode(WAL)")
	// NORMAL is the standard durability/throughput trade for WAL: safe against
	// process crash, and only at risk from OS-level power loss.
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	// If a lock is somehow contended, wait rather than fail immediately.
	q.Add("_pragma", "busy_timeout(5000)")

	dsn := "file:" + path + "?" + q.Encode()
	if isWriter {
		// BEGIN IMMEDIATE takes the write lock up front. Without this a
		// transaction that reads first and writes later must upgrade its lock
		// mid-flight, which SQLite can only refuse -- the classic, intermittent
		// SQLITE_BUSY that appears under load and never in testing.
		dsn += "&_txlock=immediate"
	}
	return sql.Open("sqlite", dsn)
}

// Reader exposes the read pool for query-only paths.
func (db *DB) Reader() *sql.DB { return db.reader }

// Now returns the store's clock. Tests substitute it via SetClock.
func (db *DB) Now() time.Time { return db.now().UTC() }

// SetClock injects a deterministic clock for tests.
func (db *DB) SetClock(fn func() time.Time) { db.now = fn }

func (db *DB) Close() error {
	var first error
	if err := db.reader.Close(); err != nil {
		first = err
	}
	if err := db.writer.Close(); err != nil && first == nil {
		first = err
	}
	return first
}
