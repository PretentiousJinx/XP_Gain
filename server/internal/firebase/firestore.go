package firebase

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"github.com/PretentiousJinx/xpgain/server/internal/store"
)

// DefaultFirestoreHost is Google's Firestore REST endpoint.
const DefaultFirestoreHost = "https://firestore.googleapis.com"

// maxWritesPerCommit is Firestore's documented ceiling for a single commit.
const maxWritesPerCommit = 500

// Firestore pushes swept rows into Cloud Firestore.
//
// Safe for concurrent use: the only shared state is the token source, which is
// itself synchronised.
type Firestore struct {
	ProjectID string
	Host      string
	Database  string
	Token     Tokener
	Client    *http.Client

	// MaxAttempts bounds retries of a single batch.
	MaxAttempts int
	now         func() time.Time
	sleep       func(context.Context, time.Duration) error
}

// NewFirestore builds a Firestore sync client.
func NewFirestore(projectID string, token Tokener) *Firestore {
	return &Firestore{
		ProjectID:   projectID,
		Host:        DefaultFirestoreHost,
		Database:    "(default)",
		Token:       token,
		Client:      &http.Client{Timeout: 30 * time.Second},
		MaxAttempts: 4,
		now:         time.Now,
		sleep:       sleepCtx,
	}
}

// docPath maps a SQLite row onto a Firestore document path.
//
// Everything is filed under the owning user, which makes security rules a
// single ownership check (`request.auth.uid == uid`) rather than a per-table
// policy, and makes a user's whole record deletable as one subtree.
func docPath(row store.DirtyRow) (string, error) {
	pk, _, ok := store.SyncedTable(row.Table)
	if !ok {
		return "", fmt.Errorf("unknown table %q", row.Table)
	}
	if row.OwnerUID == "" {
		return "", fmt.Errorf("row in %q has no owner uid", row.Table)
	}
	id := row.DocID(pk)

	switch row.Table {
	case "users":
		return "users/" + row.OwnerUID, nil
	case "characters":
		// One character per user, so a fixed document ID keeps it a singleton
		// rather than accumulating one document per sweep.
		return "users/" + row.OwnerUID + "/character/state", nil
	case "streaks":
		return "users/" + row.OwnerUID + "/streak/state", nil
	case "macro_entries":
		return "users/" + row.OwnerUID + "/entries/" + id, nil
	case "intake_rejections":
		return "users/" + row.OwnerUID + "/rejections/" + id, nil
	case "daily_macro_totals":
		return "users/" + row.OwnerUID + "/dailyTotals/" + id, nil
	default:
		return "", fmt.Errorf("no document mapping for table %q", row.Table)
	}
}

type commitRequest struct {
	Writes []writeOp `json:"writes"`
}

type writeOp struct {
	Update *document `json:"update,omitempty"`
}

type document struct {
	Name   string           `json:"name"`
	Fields map[string]Value `json:"fields"`
}

// Push ships every row, chunked to Firestore's per-commit limit.
//
// Writes are full-document overwrites keyed by a deterministic path, so the
// operation is idempotent: re-sending a batch after an ambiguous failure
// converges on the same state rather than duplicating anything. That is what
// makes it safe for SweepOnce to retry a batch whose outcome it never learned.
func (f *Firestore) Push(ctx context.Context, rows []store.DirtyRow) error {
	if len(rows) == 0 {
		return nil
	}

	writes := make([]writeOp, 0, len(rows))
	for _, row := range rows {
		path, err := docPath(row)
		if err != nil {
			return fmt.Errorf("map row to document: %w", err)
		}
		writes = append(writes, writeOp{Update: &document{
			Name:   f.documentName(path),
			Fields: EncodeFields(row.Fields),
		}})
	}

	for start := 0; start < len(writes); start += maxWritesPerCommit {
		end := min(start+maxWritesPerCommit, len(writes))
		if err := f.commit(ctx, writes[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (f *Firestore) documentName(path string) string {
	db := f.Database
	if db == "" {
		db = "(default)"
	}
	return fmt.Sprintf("projects/%s/databases/%s/documents/%s", f.ProjectID, db, path)
}

func (f *Firestore) commitURL() string {
	host := f.Host
	if host == "" {
		host = DefaultFirestoreHost
	}
	db := f.Database
	if db == "" {
		db = "(default)"
	}
	return fmt.Sprintf("%s/v1/projects/%s/databases/%s/documents:commit", host, f.ProjectID, db)
}

func (f *Firestore) commit(ctx context.Context, writes []writeOp) error {
	body, err := json.Marshal(commitRequest{Writes: writes})
	if err != nil {
		return fmt.Errorf("encode commit: %w", err)
	}

	attempts := f.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		retryAfter, err := f.commitOnce(ctx, body)
		if err == nil {
			return nil
		}
		lastErr = err

		if retryAfter < 0 || attempt == attempts {
			// retryAfter < 0 marks a permanent failure: retrying a malformed
			// document or a rejected credential just burns quota.
			break
		}
		wait := retryAfter
		if wait == 0 {
			wait = backoff(attempt)
		}
		slog.Warn("firestore commit failed; retrying",
			"attempt", attempt, "wait", wait.String(), "err", err)
		if serr := f.sleepFor(ctx, wait); serr != nil {
			return serr
		}
	}
	return fmt.Errorf("firestore commit: %w", lastErr)
}

// commitOnce returns the retry delay to use, or -1 when the failure is permanent.
func (f *Firestore) commitOnce(ctx context.Context, body []byte) (time.Duration, error) {
	token, err := f.Token.Token(ctx)
	if err != nil {
		return 0, fmt.Errorf("access token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.commitURL(), bytes.NewReader(body))
	if err != nil {
		return -1, fmt.Errorf("build commit request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err // network errors are worth retrying
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return 0, nil
	}

	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	err = fmt.Errorf("firestore returned %s: %s", resp.Status, bytes.TrimSpace(snippet))

	switch {
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		return parseRetryAfter(resp.Header.Get("Retry-After")), err
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		// Credentials will not fix themselves within a retry window.
		return -1, err
	default:
		return -1, err
	}
}

func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}

// backoff grows exponentially with jitter, so a fleet of servers recovering
// from the same outage does not resynchronise into a thundering herd.
func backoff(attempt int) time.Duration {
	base := time.Duration(1<<uint(attempt-1)) * 250 * time.Millisecond
	if base > 8*time.Second {
		base = 8 * time.Second
	}
	return base + time.Duration(rand.Int63n(int64(base/2+1)))
}

func (f *Firestore) sleepFor(ctx context.Context, d time.Duration) error {
	if f.sleep != nil {
		return f.sleep(ctx, d)
	}
	return sleepCtx(ctx, d)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
