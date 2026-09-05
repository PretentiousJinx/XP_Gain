package service_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "time/tzdata"

	"github.com/PretentiousJinx/xpgain/server/internal/domain"
	"github.com/PretentiousJinx/xpgain/server/internal/service"
	"github.com/PretentiousJinx/xpgain/server/internal/store"
	"github.com/PretentiousJinx/xpgain/server/internal/vision"
)

const testUser = "u_test"

func newTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"), 4)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	now := db.Now().Format(time.RFC3339Nano)
	err = db.WithTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`
			INSERT INTO users (id, timezone, goal_kcal, goal_protein_g, goal_carbs_g, goal_fat_g,
			                   created_at, updated_at, is_synced)
			VALUES (?, 'America/Los_Angeles', 2000, 150, 200, 65, ?, ?, 1)`,
			testUser, now, now); err != nil {
			return err
		}
		_, err := tx.Exec(`
			INSERT INTO characters (user_id, level, xp, con_micro, vit_micro, updated_at, is_synced)
			VALUES (?, 1, 0, 5000, 5000, ?, 1)`, testUser, now)
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return db
}

func validPayload(kcal, protein int) vision.Payload {
	return vision.Payload{
		IsValidFood: true,
		KCal:        kcal,
		ProteinG:    protein,
		CarbsG:      40,
		FatG:        15,
		Confidence:  0.93,
		Model:       "test-vision-1",
	}
}

// --- Path A ----------------------------------------------------------------

func TestPathACommitsEverythingAtomically(t *testing.T) {
	db := newTestDB(t)
	svc := service.New(db)

	res, err := svc.SubmitPhoto(context.Background(), service.PhotoRequest{
		UserID:        testUser,
		ClientEntryID: "c1",
		PhotoURI:      "file://meal.jpg",
		Payload:       validPayload(800, 60),
	})
	if err != nil {
		t.Fatalf("SubmitPhoto: %v", err)
	}

	if res.IsManual {
		t.Error("photo entry must not be flagged manual")
	}
	if res.Source != domain.SourcePhoto {
		t.Errorf("source = %q, want photo", res.Source)
	}
	if res.DayTotals.KCal != 800 || res.DayTotals.ProteinG != 60 {
		t.Errorf("day totals = %+v, want 800 kcal / 60 g protein", res.DayTotals)
	}
	if res.Remaining.KCal != 1200 {
		t.Errorf("remaining kcal = %d, want 1200", res.Remaining.KCal)
	}
	// 60 g protein * 6 micro = 360 micro on top of the 5000 seed = 5.36 -> 5.
	if res.Character.CON != 5 {
		t.Errorf("CON = %d, want 5", res.Character.CON)
	}
	if res.Streak.Current != 1 {
		t.Errorf("streak = %d, want 1 on first entry", res.Streak.Current)
	}
	if res.XPAwarded == 0 {
		t.Error("expected XP for a first entry of the day")
	}

	// Every row the transaction touched must be enrolled in the Firebase sweep.
	for _, table := range []string{"macro_entries", "characters", "streaks", "daily_macro_totals"} {
		var dirty int
		q := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE is_synced = 0`, table)
		if err := db.Reader().QueryRow(q).Scan(&dirty); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if dirty == 0 {
			t.Errorf("%s has no dirty row; it would be missed by the sweep", table)
		}
	}
}

func TestStatsAccumulateAcrossEntries(t *testing.T) {
	db := newTestDB(t)
	svc := service.New(db)
	ctx := context.Background()

	for i, p := range []int{50, 50, 50} {
		if _, err := svc.SubmitPhoto(ctx, service.PhotoRequest{
			UserID:        testUser,
			ClientEntryID: fmt.Sprintf("c%d", i),
			Payload:       validPayload(600, p),
		}); err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
	}

	var conMicro int
	err := db.Reader().QueryRow(
		`SELECT con_micro FROM characters WHERE user_id = ?`, testUser).Scan(&conMicro)
	if err != nil {
		t.Fatal(err)
	}
	// 150 g of protein is exactly the goal: 150 * 6 = 900 micro over the 5000 seed.
	if want := 5000 + 900; conMicro != want {
		t.Errorf("con_micro = %d, want %d", conMicro, want)
	}
}

// --- Path B ----------------------------------------------------------------

func TestPathBReturnsReasoningAndPersistsRejection(t *testing.T) {
	db := newTestDB(t)
	svc := service.New(db)

	_, err := svc.SubmitPhoto(context.Background(), service.PhotoRequest{
		UserID:        testUser,
		ClientEntryID: "c1",
		Payload: vision.Payload{
			IsValidFood:         false,
			ValidationReasoning: "This appears to be a photo of a desk, not a meal.",
			Model:               "test-vision-1",
		},
	})

	rejected, ok := service.AsRejected(err)
	if !ok {
		t.Fatalf("expected a RejectedError, got %v", err)
	}
	if rejected.ValidationReasoning != "This appears to be a photo of a desk, not a meal." {
		t.Errorf("reasoning not surfaced verbatim: %q", rejected.ValidationReasoning)
	}
	if rejected.RejectionID == "" {
		t.Fatal("rejection must carry an id the client can retry against")
	}

	// A rejection must not touch the food log or the character.
	var entries, conMicro int
	db.Reader().QueryRow(`SELECT COUNT(*) FROM macro_entries`).Scan(&entries)
	db.Reader().QueryRow(`SELECT con_micro FROM characters WHERE user_id = ?`, testUser).Scan(&conMicro)
	if entries != 0 {
		t.Errorf("rejected photo wrote %d entries, want 0", entries)
	}
	if conMicro != 5000 {
		t.Errorf("rejected photo moved CON to %d, want it untouched at 5000", conMicro)
	}
}

func TestLowConfidenceIsRejectedEvenWhenFlaggedValid(t *testing.T) {
	db := newTestDB(t)
	svc := service.New(db)

	p := validPayload(700, 40)
	p.Confidence = 0.20 // model says food, but is guessing

	_, err := svc.SubmitPhoto(context.Background(), service.PhotoRequest{
		UserID: testUser, ClientEntryID: "c1", Payload: p,
	})
	if _, ok := service.AsRejected(err); !ok {
		t.Fatalf("sub-threshold confidence must reject, got %v", err)
	}
}

// --- Fallback workflow -----------------------------------------------------

func TestManualOverrideResolvesRejectionAndFlagsPedigree(t *testing.T) {
	db := newTestDB(t)
	svc := service.New(db)
	ctx := context.Background()

	_, err := svc.SubmitPhoto(ctx, service.PhotoRequest{
		UserID: testUser, ClientEntryID: "c1",
		Payload: vision.Payload{IsValidFood: false, ValidationReasoning: "Not food."},
	})
	rejected, ok := service.AsRejected(err)
	if !ok {
		t.Fatalf("setup: expected rejection, got %v", err)
	}

	res, err := svc.SubmitManual(ctx, service.ManualRequest{
		UserID:                testUser,
		ClientEntryID:         "c1-manual",
		Macros:                domain.Macros{KCal: 650, ProteinG: 45, CarbsG: 60, FatG: 20},
		SupersedesRejectionID: rejected.RejectionID,
	})
	if err != nil {
		t.Fatalf("SubmitManual: %v", err)
	}

	if !res.IsManual || res.Source != domain.SourceManual {
		t.Errorf("pedigree wrong: is_manual=%v source=%q", res.IsManual, res.Source)
	}

	var isManual int
	var supersedes sql.NullString
	err = db.Reader().QueryRow(
		`SELECT is_manual, supersedes_rejection_id FROM macro_entries WHERE id = ?`,
		res.EntryID).Scan(&isManual, &supersedes)
	if err != nil {
		t.Fatal(err)
	}
	if isManual != 1 {
		t.Error("is_manual must be 1 in SQLite for PvP pedigree checks")
	}
	if supersedes.String != rejected.RejectionID {
		t.Error("manual entry must link back to the rejection it answered")
	}

	var resolvedBy sql.NullString
	db.Reader().QueryRow(
		`SELECT resolved_by_entry_id FROM intake_rejections WHERE id = ?`,
		rejected.RejectionID).Scan(&resolvedBy)
	if resolvedBy.String != res.EntryID {
		t.Errorf("rejection resolved_by = %q, want %q", resolvedBy.String, res.EntryID)
	}
}

func TestPhotoReattemptIsMarkedAsSuch(t *testing.T) {
	db := newTestDB(t)
	svc := service.New(db)
	ctx := context.Background()

	_, err := svc.SubmitPhoto(ctx, service.PhotoRequest{
		UserID: testUser, ClientEntryID: "c1",
		Payload: vision.Payload{IsValidFood: false, ValidationReasoning: "Too blurry."},
	})
	rejected, _ := service.AsRejected(err)

	res, err := svc.SubmitPhoto(ctx, service.PhotoRequest{
		UserID:                testUser,
		ClientEntryID:         "c1-retry",
		Payload:               validPayload(500, 30),
		SupersedesRejectionID: rejected.RejectionID,
	})
	if err != nil {
		t.Fatalf("reattempt: %v", err)
	}
	if res.Source != domain.SourceReattempt {
		t.Errorf("source = %q, want reattempt", res.Source)
	}
	if res.IsManual {
		t.Error("a reattempt is still AI-verified and must not be flagged manual")
	}
}

func TestRejectionCannotBeResolvedTwice(t *testing.T) {
	db := newTestDB(t)
	svc := service.New(db)
	ctx := context.Background()

	_, err := svc.SubmitPhoto(ctx, service.PhotoRequest{
		UserID: testUser, ClientEntryID: "c1",
		Payload: vision.Payload{IsValidFood: false, ValidationReasoning: "Not food."},
	})
	rejected, _ := service.AsRejected(err)

	base := service.ManualRequest{
		UserID: testUser, Macros: domain.Macros{KCal: 300, ProteinG: 20},
		SupersedesRejectionID: rejected.RejectionID,
	}
	base.ClientEntryID = "m1"
	if _, err := svc.SubmitManual(ctx, base); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	base.ClientEntryID = "m2"
	if _, err := svc.SubmitManual(ctx, base); err == nil {
		t.Error("a second entry must not be able to re-resolve the same rejection")
	}
}

// --- Idempotency -----------------------------------------------------------

func TestReplayedSubmitDoesNotDoubleCount(t *testing.T) {
	db := newTestDB(t)
	svc := service.New(db)
	ctx := context.Background()

	req := service.PhotoRequest{
		UserID: testUser, ClientEntryID: "same-key", Payload: validPayload(900, 70),
	}
	first, err := svc.SubmitPhoto(ctx, req)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.SubmitPhoto(ctx, req)
	if err != nil {
		t.Fatalf("replay must succeed, not error: %v", err)
	}

	if !second.Replayed {
		t.Error("replayed submit should be flagged")
	}
	if second.EntryID != first.EntryID {
		t.Error("replay must return the original entry id")
	}
	if second.DayTotals.KCal != 900 {
		t.Errorf("totals double-counted: %d kcal, want 900", second.DayTotals.KCal)
	}

	var n int
	db.Reader().QueryRow(`SELECT COUNT(*) FROM macro_entries`).Scan(&n)
	if n != 1 {
		t.Errorf("%d entry rows, want 1", n)
	}
}

// --- Concurrency -----------------------------------------------------------

// TestConcurrentSubmitsSerialiseCleanly is the test that actually exercises the
// two-pool design. Twenty goroutines submit at once against a single SQLite
// file; if the writer pool were not capped at one connection, this is where
// SQLITE_BUSY would surface and totals would be lost.
func TestConcurrentSubmitsSerialiseCleanly(t *testing.T) {
	db := newTestDB(t)
	svc := service.New(db)

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := svc.SubmitPhoto(context.Background(), service.PhotoRequest{
				UserID:        testUser,
				ClientEntryID: fmt.Sprintf("concurrent-%d", i),
				Payload:       validPayload(100, 5),
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent submit failed: %v", err)
	}

	var rows, kcal, protein int
	db.Reader().QueryRow(`SELECT COUNT(*) FROM macro_entries`).Scan(&rows)
	db.Reader().QueryRow(
		`SELECT kcal, protein_g FROM daily_macro_totals WHERE user_id = ?`, testUser).
		Scan(&kcal, &protein)

	if rows != n {
		t.Errorf("%d entry rows, want %d", rows, n)
	}
	// No lost updates: every goroutine's macros must appear in the running total.
	if kcal != n*100 || protein != n*5 {
		t.Errorf("totals = %d kcal / %d g, want %d / %d (lost update)",
			kcal, protein, n*100, n*5)
	}
}

// --- Sync sweep ------------------------------------------------------------

func TestSweepClearsDirtyFlags(t *testing.T) {
	db := newTestDB(t)
	svc := service.New(db)
	ctx := context.Background()

	if _, err := svc.SubmitPhoto(ctx, service.PhotoRequest{
		UserID: testUser, ClientEntryID: "c1", Payload: validPayload(500, 30),
	}); err != nil {
		t.Fatal(err)
	}

	var pushed int
	n, err := store.SweepOnce(ctx, db, store.PusherFunc(
		func(_ context.Context, rows []store.DirtyRow) error {
			pushed = len(rows)
			return nil
		}))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n == 0 || pushed == 0 {
		t.Fatal("sweep found no dirty rows after a commit")
	}

	var stillDirty int
	db.Reader().QueryRow(`SELECT COUNT(*) FROM macro_entries WHERE is_synced = 0`).Scan(&stillDirty)
	if stillDirty != 0 {
		t.Errorf("%d entries still dirty after an acknowledged sweep", stillDirty)
	}
}

func TestFailedPushLeavesRowsDirty(t *testing.T) {
	db := newTestDB(t)
	svc := service.New(db)
	ctx := context.Background()

	if _, err := svc.SubmitPhoto(ctx, service.PhotoRequest{
		UserID: testUser, ClientEntryID: "c1", Payload: validPayload(500, 30),
	}); err != nil {
		t.Fatal(err)
	}

	_, err := store.SweepOnce(ctx, db, store.PusherFunc(
		func(context.Context, []store.DirtyRow) error {
			return fmt.Errorf("firebase unreachable")
		}))
	if err == nil {
		t.Fatal("a failed push must surface an error")
	}

	var dirty int
	db.Reader().QueryRow(`SELECT COUNT(*) FROM macro_entries WHERE is_synced = 0`).Scan(&dirty)
	if dirty == 0 {
		t.Error("rows were marked synced despite the push failing; that data would be lost")
	}
}
