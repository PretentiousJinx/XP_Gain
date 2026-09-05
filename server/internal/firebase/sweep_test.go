package firebase_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PretentiousJinx/xpgain/server/internal/domain"
	"github.com/PretentiousJinx/xpgain/server/internal/firebase"
	"github.com/PretentiousJinx/xpgain/server/internal/service"
	"github.com/PretentiousJinx/xpgain/server/internal/store"
	"github.com/PretentiousJinx/xpgain/server/internal/vision"
)

const sweepUID = "u_sweep"

func seed(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "sweep.db"), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	now := db.Now().Format(time.RFC3339Nano)
	err = db.WithTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users
			(id, timezone, goal_kcal, goal_protein_g, goal_carbs_g, goal_fat_g, created_at, updated_at, is_synced)
			VALUES (?,'UTC',2000,150,200,65,?,?,1)`, sweepUID, now, now); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO characters
			(user_id, level, xp, con_micro, vit_micro, updated_at, is_synced)
			VALUES (?,1,0,5000,5000,?,1)`, sweepUID, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// recorder collects every document Firestore received, keyed by path suffix.
type recorder struct {
	mu   sync.Mutex
	docs map[string]map[string]firebase.Value
	fail bool
}

func (r *recorder) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		body, _ := io.ReadAll(req.Body)
		var c struct {
			Writes []struct {
				Update struct {
					Name   string                    `json:"name"`
					Fields map[string]firebase.Value `json:"fields"`
				} `json:"update"`
			} `json:"writes"`
		}
		if err := json.Unmarshal(body, &c); err != nil {
			t.Errorf("bad commit body: %v", err)
		}
		if r.docs == nil {
			r.docs = map[string]map[string]firebase.Value{}
		}
		for _, wr := range c.Writes {
			r.docs[wr.Update.Name] = wr.Update.Fields
		}
		w.Write([]byte(`{}`))
	}
}

// find locates a document by a fragment of its path. Collection documents end
// in their own ID, so this matches on a contained fragment rather than a suffix.
func (r *recorder) find(t *testing.T, fragment string) map[string]firebase.Value {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, fields := range r.docs {
		if strings.Contains(name, fragment) {
			return fields
		}
	}
	names := make([]string, 0, len(r.docs))
	for n := range r.docs {
		names = append(names, n)
	}
	t.Fatalf("no document containing %q; got %v", fragment, names)
	return nil
}

func newFS(t *testing.T, rec *recorder) *firebase.Firestore {
	t.Helper()
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)
	fs := firebase.NewFirestore("test-project", firebase.StaticToken("tok"))
	fs.Host = srv.URL
	return fs
}

func TestSweepShipsRealEntryDataAndClearsFlags(t *testing.T) {
	db := seed(t)
	svc := service.New(db)
	ctx := context.Background()

	if _, err := svc.SubmitManual(ctx, service.ManualRequest{
		UserID:        sweepUID,
		ClientEntryID: "c1",
		Macros:        domain.Macros{KCal: 650, ProteinG: 45, CarbsG: 60, FatG: 20},
	}); err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}
	n, err := store.SweepOnce(ctx, db, newFS(t, rec))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n == 0 {
		t.Fatal("sweep shipped nothing")
	}

	// The entry document must carry the actual macros, not just its key.
	entry := rec.find(t, "/entries/")
	if entry == nil {
		t.Fatal("no entry document")
	}

	// Character and streak must arrive as user-scoped singletons.
	char := rec.find(t, "/character/state")
	if got := char["level"]["integerValue"]; got != "1" {
		t.Errorf("character level = %#v, want \"1\"", char["level"])
	}
	streak := rec.find(t, "/streak/state")
	if got := streak["current_streak"]["integerValue"]; got != "1" {
		t.Errorf("streak = %#v, want \"1\"", streak["current_streak"])
	}

	// Everything acknowledged must now be clean.
	for _, table := range []string{"macro_entries", "characters", "streaks", "daily_macro_totals"} {
		var dirty int
		q := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE is_synced = 0`, table)
		db.Reader().QueryRow(q).Scan(&dirty)
		if dirty != 0 {
			t.Errorf("%s still has %d dirty rows after an acknowledged sweep", table, dirty)
		}
	}
}

func TestSweptEntryCarriesItsMacrosAndPedigree(t *testing.T) {
	db := seed(t)
	svc := service.New(db)
	ctx := context.Background()

	if _, err := svc.SubmitManual(ctx, service.ManualRequest{
		UserID:        sweepUID,
		ClientEntryID: "c1",
		Macros:        domain.Macros{KCal: 650, ProteinG: 45, CarbsG: 60, FatG: 20},
	}); err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}
	if _, err := store.SweepOnce(ctx, db, newFS(t, rec)); err != nil {
		t.Fatal(err)
	}

	entry := rec.find(t, "/entries/")
	checks := map[string]string{
		"kcal": "650", "protein_g": "45", "carbs_g": "60", "fat_g": "20",
		"is_manual": "1",
	}
	for field, want := range checks {
		if got := entry[field]["integerValue"]; got != want {
			t.Errorf("%s = %#v, want %q", field, entry[field], want)
		}
	}
	if got := entry["source"]["stringValue"]; got != "manual" {
		t.Errorf("source = %#v, want manual", entry["source"])
	}
	if _, isTimestamp := entry["logged_at"]["timestampValue"]; !isTimestamp {
		t.Errorf("logged_at = %#v, want a timestampValue", entry["logged_at"])
	}
}

func TestFailedSweepLeavesEverythingDirty(t *testing.T) {
	db := seed(t)
	svc := service.New(db)
	ctx := context.Background()

	if _, err := svc.SubmitManual(ctx, service.ManualRequest{
		UserID: sweepUID, ClientEntryID: "c1",
		Macros: domain.Macros{KCal: 500, ProteinG: 30},
	}); err != nil {
		t.Fatal(err)
	}

	rec := &recorder{fail: true}
	fs := newFS(t, rec)
	fs.MaxAttempts = 1

	if _, err := store.SweepOnce(ctx, db, fs); err == nil {
		t.Fatal("a failing push must surface an error")
	}

	var dirty int
	db.Reader().QueryRow(`SELECT COUNT(*) FROM macro_entries WHERE is_synced = 0`).Scan(&dirty)
	if dirty == 0 {
		t.Error("rows were marked synced despite the push failing; that data would be lost")
	}
}

func TestRowEditedDuringSweepStaysDirty(t *testing.T) {
	// MarkSynced guards on the updated_at it saw. If the row changed while the
	// push was in flight, clearing the flag would drop that later edit.
	db := seed(t)
	svc := service.New(db)
	ctx := context.Background()

	if _, err := svc.SubmitManual(ctx, service.ManualRequest{
		UserID: sweepUID, ClientEntryID: "c1",
		Macros: domain.Macros{KCal: 500, ProteinG: 30},
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := store.PendingSync(ctx, db, 500)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a concurrent edit landing after the rows were read.
	if _, err := svc.SubmitPhoto(ctx, service.PhotoRequest{
		UserID: sweepUID, ClientEntryID: "c2",
		Payload: vision.Payload{
			IsValidFood: true, KCal: 300, ProteinG: 20,
			Confidence: 0.9, Model: "m",
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Acknowledge only the originally-read rows.
	err = db.WithTx(ctx, func(tx *sql.Tx) error {
		for _, r := range rows {
			if err := store.MarkSynced(ctx, tx, r.Table, r.PK, r.UpdatedAt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var dirty int
	db.Reader().QueryRow(
		`SELECT COUNT(*) FROM characters WHERE is_synced = 0`).Scan(&dirty)
	if dirty != 1 {
		t.Error("the character row was re-edited mid-sweep and must remain dirty")
	}
}

func TestIsSyncedIsStrippedByTheSweepItself(t *testing.T) {
	// The encoder-level test builds its field map by hand, so it never sees the
	// filtering that actually matters -- which happens in store.PendingSync as
	// the row is read. This drives the real path: log an entry, sweep it, and
	// assert the local bookkeeping column never reaches the wire. Shipping it
	// would let a restored backup look already-synced and skip its own sweep.
	db := seed(t)
	svc := service.New(db)
	ctx := context.Background()

	if _, err := svc.SubmitManual(ctx, service.ManualRequest{
		UserID: sweepUID, ClientEntryID: "c1",
		Macros: domain.Macros{KCal: 400, ProteinG: 25},
	}); err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}
	if _, err := store.SweepOnce(ctx, db, newFS(t, rec)); err != nil {
		t.Fatal(err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.docs) == 0 {
		t.Fatal("nothing was swept")
	}
	for name, fields := range rec.docs {
		if _, present := fields["is_synced"]; present {
			t.Errorf("%s carried is_synced to Firestore", name)
		}
	}
}

func TestSweptRowsCarryEveryOtherColumn(t *testing.T) {
	// Guards the inverse of the above: the strip must remove is_synced only,
	// not quietly drop real columns.
	db := seed(t)
	svc := service.New(db)
	ctx := context.Background()

	if _, err := svc.SubmitManual(ctx, service.ManualRequest{
		UserID: sweepUID, ClientEntryID: "c1",
		Macros: domain.Macros{KCal: 400, ProteinG: 25},
	}); err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}
	if _, err := store.SweepOnce(ctx, db, newFS(t, rec)); err != nil {
		t.Fatal(err)
	}

	entry := rec.find(t, "/entries/")
	for _, col := range []string{
		"id", "user_id", "client_entry_id", "local_date", "kcal", "protein_g",
		"carbs_g", "fat_g", "source", "is_manual", "logged_at", "created_at", "updated_at",
	} {
		if _, ok := entry[col]; !ok {
			t.Errorf("column %q was dropped on the way to Firestore", col)
		}
	}
}
