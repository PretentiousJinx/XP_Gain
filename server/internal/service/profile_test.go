package service_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/PretentiousJinx/xpgain/server/internal/domain"
	"github.com/PretentiousJinx/xpgain/server/internal/service"
	"github.com/PretentiousJinx/xpgain/server/internal/store"
)

const newUID = "u_fresh"

// emptyDB has the schema but no rows: the state a user is in immediately after
// their first successful sign-in.
func emptyDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "p.db"), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func goodGoals() domain.Goals {
	return domain.Goals{KCal: 2200, ProteinG: 160, CarbsG: 220, FatG: 70}
}

func TestFirstCallProvisionsTheAccount(t *testing.T) {
	db := emptyDB(t)
	svc := service.New(db)

	p, err := svc.EnsureProfile(context.Background(), service.ProfileRequest{
		UserID: newUID, Timezone: "America/Los_Angeles", Goals: goodGoals(),
	})
	if err != nil {
		t.Fatalf("EnsureProfile: %v", err)
	}

	if !p.Created {
		t.Error("first call should report Created")
	}
	if p.Character.Level != 1 {
		t.Errorf("level = %d, want 1", p.Character.Level)
	}
	if p.Character.CON != 5 || p.Character.VIT != 5 {
		t.Errorf("starting stats = CON %d / VIT %d, want 5/5", p.Character.CON, p.Character.VIT)
	}
	if p.Streak.Current != 0 {
		t.Errorf("streak = %d, want 0", p.Streak.Current)
	}
	if p.Remaining.KCal != 2200 {
		t.Errorf("remaining = %d, want the full goal", p.Remaining.KCal)
	}
}

func TestProvisioningIsIdempotent(t *testing.T) {
	db := emptyDB(t)
	svc := service.New(db)
	ctx := context.Background()

	req := service.ProfileRequest{UserID: newUID, Timezone: "UTC", Goals: goodGoals()}
	first, err := svc.EnsureProfile(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.EnsureProfile(ctx, req)
	if err != nil {
		t.Fatalf("second call must succeed, not conflict: %v", err)
	}

	if !first.Created || second.Created {
		t.Errorf("Created flags = %v then %v, want true then false", first.Created, second.Created)
	}

	var users int
	db.Reader().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users)
	if users != 1 {
		t.Errorf("%d user rows after two calls, want 1", users)
	}
}

func TestEditingGoalsDoesNotResetProgress(t *testing.T) {
	// The regression this guards: an upsert on characters would wipe a
	// player's stats the first time they adjusted their macro goals.
	db := emptyDB(t)
	svc := service.New(db)
	ctx := context.Background()

	if _, err := svc.EnsureProfile(ctx, service.ProfileRequest{
		UserID: newUID, Timezone: "UTC", Goals: goodGoals(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SubmitManual(ctx, service.ManualRequest{
		UserID: newUID, ClientEntryID: "e1",
		Macros: domain.Macros{KCal: 900, ProteinG: 80, CarbsG: 60, FatG: 25},
	}); err != nil {
		t.Fatal(err)
	}

	before, err := svc.GetProfile(ctx, newUID)
	if err != nil {
		t.Fatal(err)
	}

	// Now change the goals, as a settings screen would.
	changed := goodGoals()
	changed.KCal = 2500
	after, err := svc.EnsureProfile(ctx, service.ProfileRequest{
		UserID: newUID, Timezone: "UTC", Goals: changed,
	})
	if err != nil {
		t.Fatal(err)
	}

	if after.Goals.KCal != 2500 {
		t.Errorf("goal not updated: %d", after.Goals.KCal)
	}
	if after.Character.CON != before.Character.CON {
		t.Errorf("CON changed from %d to %d on a goal edit",
			before.Character.CON, after.Character.CON)
	}
	if after.Character.XP != before.Character.XP {
		t.Errorf("XP changed from %d to %d on a goal edit",
			before.Character.XP, after.Character.XP)
	}
	if after.Streak.Current != before.Streak.Current {
		t.Errorf("streak changed from %d to %d on a goal edit",
			before.Streak.Current, after.Streak.Current)
	}
	if after.DayTotals.KCal != before.DayTotals.KCal {
		t.Error("day totals were lost on a goal edit")
	}
}

func TestUnprovisionedUserIsNotFound(t *testing.T) {
	db := emptyDB(t)
	svc := service.New(db)

	_, err := svc.GetProfile(context.Background(), "never-signed-up")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestIntakeBeforeProvisioningIsNotFound(t *testing.T) {
	db := emptyDB(t)
	svc := service.New(db)

	_, err := svc.SubmitManual(context.Background(), service.ManualRequest{
		UserID: "never-signed-up", ClientEntryID: "e1",
		Macros: domain.Macros{KCal: 100, ProteinG: 5},
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound so the client knows to onboard", err)
	}
}

func TestImplausibleGoalsAreRejected(t *testing.T) {
	// Goals are the denominator of every stat award. A 1 kcal goal would make
	// any snack "on target" and let a player max their stats instantly.
	db := emptyDB(t)
	svc := service.New(db)

	bad := []domain.Goals{
		{KCal: 1, ProteinG: 160, CarbsG: 220, FatG: 70},
		{KCal: 2200, ProteinG: 0, CarbsG: 220, FatG: 70},
		{KCal: 2200, ProteinG: 160, CarbsG: 220, FatG: -5},
		{KCal: 999999, ProteinG: 160, CarbsG: 220, FatG: 70},
		{},
	}
	for _, g := range bad {
		_, err := svc.EnsureProfile(context.Background(), service.ProfileRequest{
			UserID: newUID, Timezone: "UTC", Goals: g,
		})
		if !errors.Is(err, domain.ErrInvalidPayload) {
			t.Errorf("goals %+v were accepted (err = %v)", g, err)
		}
	}
}

func TestUnknownTimezoneIsRejected(t *testing.T) {
	// Silently falling back to UTC would break streaks for the life of the
	// account, and the user would never see why.
	db := emptyDB(t)
	svc := service.New(db)

	_, err := svc.EnsureProfile(context.Background(), service.ProfileRequest{
		UserID: newUID, Timezone: "Mars/Olympus_Mons", Goals: goodGoals(),
	})
	if !errors.Is(err, domain.ErrInvalidPayload) {
		t.Fatalf("err = %v, want the bad zone refused", err)
	}
}

func TestEmptyTimezoneDefaultsToUTC(t *testing.T) {
	db := emptyDB(t)
	svc := service.New(db)

	p, err := svc.EnsureProfile(context.Background(), service.ProfileRequest{
		UserID: newUID, Goals: goodGoals(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Timezone != "UTC" {
		t.Errorf("timezone = %q, want UTC", p.Timezone)
	}
}

func TestProvisionedRowsAreEnrolledInTheSweep(t *testing.T) {
	db := emptyDB(t)
	svc := service.New(db)

	if _, err := svc.EnsureProfile(context.Background(), service.ProfileRequest{
		UserID: newUID, Timezone: "UTC", Goals: goodGoals(),
	}); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"users", "characters", "streaks"} {
		var dirty int
		db.Reader().QueryRow(
			`SELECT COUNT(*) FROM ` + table + ` WHERE is_synced = 0`).Scan(&dirty)
		if dirty == 0 {
			t.Errorf("%s was not flagged for the Firebase sweep", table)
		}
	}
}

func TestProfileReflectsLoggedFood(t *testing.T) {
	db := emptyDB(t)
	svc := service.New(db)
	ctx := context.Background()

	if _, err := svc.EnsureProfile(ctx, service.ProfileRequest{
		UserID: newUID, Timezone: "UTC", Goals: goodGoals(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SubmitManual(ctx, service.ManualRequest{
		UserID: newUID, ClientEntryID: "e1",
		Macros: domain.Macros{KCal: 700, ProteinG: 50, CarbsG: 60, FatG: 20},
	}); err != nil {
		t.Fatal(err)
	}

	p, err := svc.GetProfile(ctx, newUID)
	if err != nil {
		t.Fatal(err)
	}
	if p.DayTotals.KCal != 700 {
		t.Errorf("day totals = %d, want 700", p.DayTotals.KCal)
	}
	if p.Remaining.KCal != 1500 {
		t.Errorf("remaining = %d, want 1500", p.Remaining.KCal)
	}
	if p.Streak.Current != 1 {
		t.Errorf("streak = %d, want 1", p.Streak.Current)
	}
}
