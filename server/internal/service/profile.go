package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/PretentiousJinx/xpgain/server/internal/domain"
	"github.com/PretentiousJinx/xpgain/server/internal/store"
)

// ProfileRequest provisions or updates the signed-in user.
type ProfileRequest struct {
	UserID   string
	Timezone string
	Goals    domain.Goals
}

// Profile is the client's view of its own account.
type Profile struct {
	UserID    string        `json:"user_id"`
	Timezone  string        `json:"timezone"`
	Goals     domain.Goals  `json:"goals"`
	LocalDate string        `json:"local_date"`
	DayTotals domain.Macros `json:"day_totals"`
	Remaining domain.Macros `json:"remaining"`
	Character CharacterView `json:"character"`
	Streak    domain.Streak `json:"streak"`
	Created   bool          `json:"created"`
}

// EnsureProfile creates the account on first call and updates goals thereafter.
//
// It is idempotent by construction, which is what lets the mobile client call
// it unconditionally at launch instead of having to know whether it has ever
// signed in on this device. The user row is upserted; the character and streak
// rows are created only if absent, so editing macro goals never resets a
// player's progress.
//
// The UID is never taken from the request body -- it comes from the verified
// token -- so this cannot be used to provision or overwrite another account.
func (s *Service) EnsureProfile(ctx context.Context, req ProfileRequest) (*Profile, error) {
	if req.UserID == "" {
		return nil, fmt.Errorf("%w: user_id is required", domain.ErrInvalidPayload)
	}
	if ok, why := req.Goals.Valid(); !ok {
		return nil, fmt.Errorf("%w: %s", domain.ErrInvalidPayload, why)
	}

	// Reject an unknown zone rather than silently falling back to UTC. The
	// intake path derives every local_date and streak boundary from this, so a
	// typo here would quietly break streaks for the life of the account.
	tz := req.Timezone
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return nil, fmt.Errorf("%w: unknown timezone %q", domain.ErrInvalidPayload, tz)
	}

	now := s.db.Now()
	var out Profile

	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		existed, err := store.UserExists(ctx, tx, req.UserID)
		if err != nil {
			return err
		}
		if err := store.UpsertUser(ctx, tx, req.UserID, tz, req.Goals, now); err != nil {
			return err
		}
		if err := store.EnsureCharacter(ctx, tx, req.UserID, now); err != nil {
			return err
		}
		if err := store.EnsureStreak(ctx, tx, req.UserID, now); err != nil {
			return err
		}
		out.Created = !existed
		return s.loadProfile(ctx, tx, req.UserID, &out)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetProfile returns the current account state, or ErrNotFound if the user has
// not been provisioned yet.
func (s *Service) GetProfile(ctx context.Context, userID string) (*Profile, error) {
	if userID == "" {
		return nil, fmt.Errorf("%w: user_id is required", domain.ErrInvalidPayload)
	}
	var out Profile
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		return s.loadProfile(ctx, tx, userID, &out)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// loadProfile assembles the account view from within an open transaction, so
// the goals, stats, streak and day totals it reports are mutually consistent.
func (s *Service) loadProfile(ctx context.Context, tx *sql.Tx, userID string, out *Profile) error {
	user, err := store.LoadUserProfile(ctx, tx, userID)
	if err != nil {
		return err
	}
	char, err := store.LoadCharacter(ctx, tx, userID)
	if err != nil {
		return err
	}
	streak, _, err := store.LoadStreak(ctx, tx, userID)
	if err != nil {
		return err
	}

	localDate := s.db.Now().In(loadLocation(user.Timezone)).Format("2006-01-02")
	totals, _, err := store.LoadDailyTotals(ctx, tx, userID, localDate)
	if err != nil {
		return err
	}

	created := out.Created
	*out = Profile{
		UserID:    user.ID,
		Timezone:  user.Timezone,
		Goals:     user.Goals,
		LocalDate: localDate,
		DayTotals: totals,
		Remaining: remaining(user.Goals, totals),
		Character: viewOf(char),
		Streak:    streak,
		Created:   created,
	}
	return nil
}
