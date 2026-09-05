package firebase

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PretentiousJinx/xpgain/server/internal/store"
)

func row(table, owner string, pk map[string]any, fields map[string]any) store.DirtyRow {
	return store.DirtyRow{
		Table: table, OwnerUID: owner, PK: pk,
		UpdatedAt: "2026-09-05T12:00:00Z", Fields: fields,
	}
}

// captured is one decoded commit body.
type captured struct {
	Writes []struct {
		Update struct {
			Name   string           `json:"name"`
			Fields map[string]Value `json:"fields"`
		} `json:"update"`
	} `json:"writes"`
}

func newFirestoreTo(t *testing.T, h http.HandlerFunc) (*Firestore, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	fs := NewFirestore("test-project", StaticToken("access-token"))
	fs.Host = srv.URL
	// Collapse backoff so retry tests do not actually sleep.
	fs.sleep = func(context.Context, time.Duration) error { return nil }
	return fs, srv
}

// --- value encoding --------------------------------------------------------

func TestIntegersAreEncodedAsStrings(t *testing.T) {
	// Firestore requires integerValue to be a JSON string. Sending a number
	// silently truncates large int64s through JSON's float64.
	fields := EncodeFields(map[string]any{"kcal": int64(1800), "level": 7})

	if got := fields["kcal"]["integerValue"]; got != "1800" {
		t.Errorf("kcal = %#v, want the string \"1800\"", got)
	}
	if _, isString := fields["level"]["integerValue"].(string); !isString {
		t.Errorf("level integerValue is %T, want string", fields["level"]["integerValue"])
	}
}

func TestWholeFloatsStayIntegers(t *testing.T) {
	// SQLite can return INTEGER columns as float64. A level of 7 must not
	// arrive as 7.0 and break a strict client decoder.
	fields := EncodeFields(map[string]any{"level": float64(7), "confidence": 0.93})

	if got := fields["level"]["integerValue"]; got != "7" {
		t.Errorf("whole float encoded as %#v, want integerValue \"7\"", fields["level"])
	}
	if got := fields["confidence"]["doubleValue"]; got != 0.93 {
		t.Errorf("confidence = %#v, want doubleValue 0.93", fields["confidence"])
	}
}

func TestTimestampColumnsBecomeTimestamps(t *testing.T) {
	fields := EncodeFields(map[string]any{
		"updated_at": "2026-09-05T12:00:00.5Z",
		"local_date": "2026-09-05",
	})

	if _, ok := fields["updated_at"]["timestampValue"]; !ok {
		t.Errorf("updated_at encoded as %#v, want a timestampValue", fields["updated_at"])
	}
	// local_date is a calendar date in the user's zone, not an instant.
	// Coercing it would silently re-anchor it to UTC midnight.
	if got := fields["local_date"]["stringValue"]; got != "2026-09-05" {
		t.Errorf("local_date encoded as %#v, want a stringValue", fields["local_date"])
	}
}

func TestNullsAndUnparseableTimestampsSurvive(t *testing.T) {
	fields := EncodeFields(map[string]any{
		"photo_uri":  nil,
		"created_at": "not a timestamp",
	})

	if _, ok := fields["photo_uri"]["nullValue"]; !ok {
		t.Errorf("nil encoded as %#v, want nullValue", fields["photo_uri"])
	}
	if got := fields["created_at"]["stringValue"]; got != "not a timestamp" {
		t.Errorf("unparseable timestamp should fall back to a string, got %#v", fields["created_at"])
	}
}

// --- document mapping ------------------------------------------------------

func TestDocumentPathsAreUserScoped(t *testing.T) {
	tests := []struct {
		table string
		pk    map[string]any
		want  string
	}{
		{"users", map[string]any{"id": "u1"}, "users/u1"},
		{"characters", map[string]any{"user_id": "u1"}, "users/u1/character/state"},
		{"streaks", map[string]any{"user_id": "u1"}, "users/u1/streak/state"},
		{"macro_entries", map[string]any{"id": "e9"}, "users/u1/entries/e9"},
		{"intake_rejections", map[string]any{"id": "r3"}, "users/u1/rejections/r3"},
		{"daily_macro_totals",
			map[string]any{"user_id": "u1", "local_date": "2026-09-05"},
			"users/u1/dailyTotals/u1_2026-09-05"},
	}

	for _, tt := range tests {
		got, err := docPath(row(tt.table, "u1", tt.pk, nil))
		if err != nil {
			t.Errorf("%s: %v", tt.table, err)
			continue
		}
		if got != tt.want {
			t.Errorf("%s -> %q, want %q", tt.table, got, tt.want)
		}
	}
}

func TestSingletonTablesGetStableDocumentIDs(t *testing.T) {
	// Character and streak are one-per-user. If their paths varied, each sweep
	// would append a new document instead of overwriting the last.
	a, _ := docPath(row("characters", "u1", map[string]any{"user_id": "u1"}, nil))
	b, _ := docPath(row("characters", "u1", map[string]any{"user_id": "u1"}, nil))
	if a != b {
		t.Errorf("character path is not stable: %q vs %q", a, b)
	}
}

func TestRowWithoutOwnerIsRefused(t *testing.T) {
	if _, err := docPath(row("macro_entries", "", map[string]any{"id": "e1"}, nil)); err == nil {
		t.Fatal("a row with no owner uid must not be filed anywhere")
	}
}

// --- commit ----------------------------------------------------------------

func TestPushSendsFullRowContents(t *testing.T) {
	// This is the test that would have caught DirtyRow shipping only keys:
	// it asserts the actual macro values reach the wire.
	var got captured
	fs, _ := newFirestoreTo(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)
		w.Write([]byte(`{}`))
	})

	err := fs.Push(context.Background(), []store.DirtyRow{
		row("macro_entries", "u1", map[string]any{"id": "e1"}, map[string]any{
			"id": "e1", "user_id": "u1", "kcal": int64(650), "protein_g": int64(45),
			"is_manual": int64(1), "local_date": "2026-09-05",
		}),
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	if len(got.Writes) != 1 {
		t.Fatalf("%d writes, want 1", len(got.Writes))
	}
	f := got.Writes[0].Update.Fields
	if f["kcal"]["integerValue"] != "650" {
		t.Errorf("kcal did not reach the wire: %#v", f["kcal"])
	}
	if f["protein_g"]["integerValue"] != "45" {
		t.Errorf("protein_g did not reach the wire: %#v", f["protein_g"])
	}
	if f["is_manual"]["integerValue"] != "1" {
		t.Errorf("pedigree flag did not reach the wire: %#v", f["is_manual"])
	}
	if !strings.HasSuffix(got.Writes[0].Update.Name,
		"/documents/users/u1/entries/e1") {
		t.Errorf("document name = %q", got.Writes[0].Update.Name)
	}
}

func TestIsSyncedIsNotShipped(t *testing.T) {
	// is_synced is local bookkeeping. Shipping it would let a restored backup
	// look already-synced and skip its own sweep.
	var got captured
	fs, _ := newFirestoreTo(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)
		w.Write([]byte(`{}`))
	})

	fs.Push(context.Background(), []store.DirtyRow{
		row("characters", "u1", map[string]any{"user_id": "u1"},
			map[string]any{"user_id": "u1", "level": int64(3)}),
	})

	if _, present := got.Writes[0].Update.Fields["is_synced"]; present {
		t.Error("is_synced was shipped to Firestore")
	}
}

func TestLargeBatchesAreChunked(t *testing.T) {
	var commits int32
	var total int32
	fs, _ := newFirestoreTo(t, func(w http.ResponseWriter, r *http.Request) {
		var c captured
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &c)
		if len(c.Writes) > maxWritesPerCommit {
			t.Errorf("commit carried %d writes, over the %d limit",
				len(c.Writes), maxWritesPerCommit)
		}
		atomic.AddInt32(&commits, 1)
		atomic.AddInt32(&total, int32(len(c.Writes)))
		w.Write([]byte(`{}`))
	})

	const n = 1200
	rows := make([]store.DirtyRow, n)
	for i := range rows {
		rows[i] = row("macro_entries", "u1",
			map[string]any{"id": fmt.Sprintf("e%d", i)},
			map[string]any{"id": fmt.Sprintf("e%d", i)})
	}

	if err := fs.Push(context.Background(), rows); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if got := atomic.LoadInt32(&commits); got != 3 {
		t.Errorf("%d commits for %d rows, want 3", got, n)
	}
	if got := atomic.LoadInt32(&total); got != n {
		t.Errorf("%d writes delivered, want %d", got, n)
	}
}

func TestTransientFailuresAreRetried(t *testing.T) {
	var attempts int32
	fs, _ := newFirestoreTo(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{}`))
	})

	err := fs.Push(context.Background(), []store.DirtyRow{
		row("users", "u1", map[string]any{"id": "u1"}, map[string]any{"id": "u1"}),
	})
	if err != nil {
		t.Fatalf("Push should have recovered: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("%d attempts, want 3", got)
	}
}

func TestCredentialFailuresAreNotRetried(t *testing.T) {
	// Retrying a rejected credential just burns quota; it will not start
	// working within a backoff window.
	var attempts int32
	fs, _ := newFirestoreTo(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusForbidden)
	})

	if err := fs.Push(context.Background(), []store.DirtyRow{
		row("users", "u1", map[string]any{"id": "u1"}, map[string]any{"id": "u1"}),
	}); err == nil {
		t.Fatal("a 403 should surface as an error")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("%d attempts on a 403, want exactly 1", got)
	}
}

func TestPushFailsAfterExhaustingAttempts(t *testing.T) {
	fs, _ := newFirestoreTo(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	fs.MaxAttempts = 2

	if err := fs.Push(context.Background(), []store.DirtyRow{
		row("users", "u1", map[string]any{"id": "u1"}, map[string]any{"id": "u1"}),
	}); err == nil {
		t.Fatal("persistent 500s must surface as an error so rows stay dirty")
	}
}

func TestAuthorizationHeaderIsSent(t *testing.T) {
	var seen string
	fs, _ := newFirestoreTo(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.Write([]byte(`{}`))
	})

	fs.Push(context.Background(), []store.DirtyRow{
		row("users", "u1", map[string]any{"id": "u1"}, map[string]any{"id": "u1"}),
	})

	if seen != "Bearer access-token" {
		t.Errorf("Authorization = %q", seen)
	}
}

func TestEmptyPushIsANoop(t *testing.T) {
	var called int32
	fs, _ := newFirestoreTo(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&called, 1)
		w.Write([]byte(`{}`))
	})

	if err := fs.Push(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Error("an empty sweep should not call Firestore at all")
	}
}
