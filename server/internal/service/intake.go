// Package service holds the macro intake workflow: the routing decision, the
// fallback paths, and the one transaction that commits a successful log.
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/PretentiousJinx/xpgain/server/internal/domain"
	"github.com/PretentiousJinx/xpgain/server/internal/id"
	"github.com/PretentiousJinx/xpgain/server/internal/store"
	"github.com/PretentiousJinx/xpgain/server/internal/vision"
)

// Service is safe for concurrent use: it holds no mutable state of its own, and
// every field below is either immutable after construction or independently
// concurrency-safe. All serialisation happens in the store's writer pool.
type Service struct {
	db *store.DB
}

func New(db *store.DB) *Service { return &Service{db: db} }

// PhotoRequest is a Vision AI submission. A reattempt is the same request with
// SupersedesRejectionID set, which is why there is no separate reattempt type.
type PhotoRequest struct {
	UserID        string
	ClientEntryID string
	PhotoURI      string
	LoggedAt      time.Time
	Payload       vision.Payload

	// SupersedesRejectionID, when set, marks this as a retry of an earlier
	// refusal and links the two records.
	SupersedesRejectionID string
}

// ManualRequest is the user-typed override. It carries no AI fields at all:
// a manual entry has no confidence and no model, and pretending otherwise
// would corrupt the pedigree the PvP layer depends on.
type ManualRequest struct {
	UserID        string
	ClientEntryID string
	Macros        domain.Macros
	LoggedAt      time.Time

	SupersedesRejectionID string
}

// Result is the Path A response.
type Result struct {
	EntryID      string             `json:"entry_id"`
	Source       domain.EntrySource `json:"source"`
	IsManual     bool               `json:"is_manual"`
	LocalDate    string             `json:"local_date"`
	DayTotals    domain.Macros      `json:"day_totals"`
	Goals        domain.Goals       `json:"goals"`
	Remaining    domain.Macros      `json:"remaining"`
	Character    CharacterView      `json:"character"`
	Streak       domain.Streak      `json:"streak"`
	XPAwarded    int                `json:"xp_awarded"`
	LevelsGained int                `json:"levels_gained"`
	Replayed     bool               `json:"replayed"`
}

// CharacterView is the client-facing stat line.
type CharacterView struct {
	Level    int `json:"level"`
	XP       int `json:"xp"`
	XPToNext int `json:"xp_to_next"`
	CON      int `json:"con"`
	VIT      int `json:"vit"`
}

// SubmitPhoto is the entry point for parsed Vision AI output.
//
// It is a two-branch router and nothing more. All the interesting work lives
// either in vision.Payload.Accepted (the decision) or in commit (the effect),
// which keeps the branch itself small enough to read at a glance.
func (s *Service) SubmitPhoto(ctx context.Context, req PhotoRequest) (*Result, error) {
	if req.UserID == "" || req.ClientEntryID == "" {
		return nil, fmt.Errorf("%w: user_id and client_entry_id are required", domain.ErrInvalidPayload)
	}

	if ok, reasoning := req.Payload.Accepted(); !ok {
		// ---- Path B: rejected -------------------------------------------
		// The refusal is persisted before it is returned, so the client can
		// come back to it later with a manual override or a reattempt.
		rejectionID, err := s.recordRejection(ctx, req, reasoning)
		if err != nil {
			return nil, err
		}
		conf := req.Payload.Confidence
		return nil, &domain.RejectedError{
			RejectionID:         rejectionID,
			ValidationReasoning: reasoning,
			Confidence:          conf,
		}
	}

	// ---- Path A: accepted -----------------------------------------------
	src := domain.SourcePhoto
	if req.SupersedesRejectionID != "" {
		src = domain.SourceReattempt
	}
	conf := req.Payload.Confidence
	return s.commit(ctx, commitInput{
		UserID:        req.UserID,
		ClientEntryID: req.ClientEntryID,
		Macros:        req.Payload.Macros(),
		Source:        src,
		LoggedAt:      req.LoggedAt,
		AIConfidence:  &conf,
		AIModel:       req.Payload.Model,
		PhotoURI:      req.PhotoURI,
		SupersedesID:  req.SupersedesRejectionID,
	})
}

// SubmitManual is the fallback path: the user types the macros themselves.
// The record is flagged is_manual so downstream trust decisions can see that
// no vision model ever confirmed these numbers.
func (s *Service) SubmitManual(ctx context.Context, req ManualRequest) (*Result, error) {
	if req.UserID == "" || req.ClientEntryID == "" {
		return nil, fmt.Errorf("%w: user_id and client_entry_id are required", domain.ErrInvalidPayload)
	}
	if !req.Macros.Valid() {
		return nil, fmt.Errorf("%w: macros outside the accepted range", domain.ErrInvalidPayload)
	}
	return s.commit(ctx, commitInput{
		UserID:        req.UserID,
		ClientEntryID: req.ClientEntryID,
		Macros:        req.Macros,
		Source:        domain.SourceManual,
		LoggedAt:      req.LoggedAt,
		SupersedesID:  req.SupersedesRejectionID,
	})
}

// recordRejection persists a Path B outcome in its own short transaction.
func (s *Service) recordRejection(ctx context.Context, req PhotoRequest, reasoning string) (string, error) {
	rec := domain.RejectionRecord{
		ID:                  id.New("rej"),
		UserID:              req.UserID,
		ClientEntryID:       req.ClientEntryID,
		ValidationReasoning: reasoning,
		AIModel:             req.Payload.Model,
		PhotoURI:            req.PhotoURI,
	}
	if req.Payload.Confidence > 0 {
		c := req.Payload.Confidence
		rec.AIConfidence = &c
	}
	now := s.db.Now()
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		return store.InsertRejection(ctx, tx, rec, now)
	})
	if err != nil {
		return "", err
	}
	return rec.ID, nil
}

type commitInput struct {
	UserID        string
	ClientEntryID string
	Macros        domain.Macros
	Source        domain.EntrySource
	LoggedAt      time.Time
	AIConfidence  *float64
	AIModel       string
	PhotoURI      string
	SupersedesID  string
}

// commit is the single write path for an accepted entry, photo or manual alike.
//
// Everything it touches moves in one transaction: the entry row, the day's
// running totals, the character's base stats, the streak with its activity
// timestamp, and the is_synced flags that enrol all of them in the next
// Firebase sweep. A partial apply here would be a character whose stats do not
// match their own food log, which is unrecoverable without a manual audit --
// so there is no path through this function that commits some of it.
func (s *Service) commit(ctx context.Context, in commitInput) (*Result, error) {
	if in.LoggedAt.IsZero() {
		in.LoggedAt = s.db.Now()
	}
	now := s.db.Now()

	var out Result
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		user, err := store.LoadUserProfile(ctx, tx, in.UserID)
		if err != nil {
			return err
		}

		// Idempotency. Mobile clients retry aggressively on flaky links, and a
		// duplicated submit must not double-count macros or double-award stats.
		if existingID, found, err := store.FindEntryByClientID(ctx, tx, in.UserID, in.ClientEntryID); err != nil {
			return err
		} else if found {
			return s.replay(ctx, tx, user, existingID, &out)
		}

		loc := loadLocation(user.Timezone)
		localDate := in.LoggedAt.In(loc).Format("2006-01-02")

		dayBefore, entryCount, err := store.LoadDailyTotals(ctx, tx, in.UserID, localDate)
		if err != nil {
			return err
		}
		char, err := store.LoadCharacter(ctx, tx, in.UserID)
		if err != nil {
			return err
		}
		streak, hadPrevious, err := store.LoadStreak(ctx, tx, in.UserID)
		if err != nil {
			return err
		}

		prog := domain.ComputeProgression(dayBefore, in.Macros, user.Goals, in.Source, entryCount == 0)
		prog.LevelsGained = char.ApplyXP(prog.XPAwarded)

		entry := domain.MacroEntry{
			ID:            id.New("ent"),
			UserID:        in.UserID,
			ClientEntryID: in.ClientEntryID,
			LocalDate:     localDate,
			Macros:        in.Macros,
			Source:        in.Source,
			IsManual:      in.Source.IsManual(),
			AIConfidence:  in.AIConfidence,
			AIModel:       in.AIModel,
			PhotoURI:      in.PhotoURI,
			SupersedesID:  in.SupersedesID,
			LoggedAt:      in.LoggedAt,
		}
		if err := store.InsertEntry(ctx, tx, entry, now); err != nil {
			return err
		}
		if err := store.UpsertDailyTotals(ctx, tx, in.UserID, localDate, prog.DayAfter, now); err != nil {
			return err
		}
		if err := store.UpdateCharacter(ctx, tx, in.UserID,
			prog.ConDelta, prog.VitDelta, char.XP, char.Level, now); err != nil {
			return err
		}

		streak = domain.NextStreak(streak, daysBetween(streak.LastLocalDate, localDate), hadPrevious)
		if err := store.UpsertStreak(ctx, tx, streak, localDate, now); err != nil {
			return err
		}

		// Close the loop on the fallback workflow: an entry that answers an
		// earlier refusal marks that refusal resolved, in the same transaction.
		if in.SupersedesID != "" {
			if err := store.ResolveRejection(ctx, tx, in.SupersedesID, entry.ID, in.UserID, now); err != nil {
				return err
			}
		}

		// Reload stats through the same transaction so the response reports
		// what was actually committed, not what we predicted.
		char, err = store.LoadCharacter(ctx, tx, in.UserID)
		if err != nil {
			return err
		}

		out = Result{
			EntryID:      entry.ID,
			Source:       entry.Source,
			IsManual:     entry.IsManual,
			LocalDate:    localDate,
			DayTotals:    prog.DayAfter,
			Goals:        user.Goals,
			Remaining:    remaining(user.Goals, prog.DayAfter),
			Character:    viewOf(char),
			Streak:       streak,
			XPAwarded:    prog.XPAwarded,
			LevelsGained: prog.LevelsGained,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// replay rebuilds a response for an entry that was already committed under this
// client_entry_id, so a retry sees the same outcome instead of an error.
func (s *Service) replay(ctx context.Context, tx *sql.Tx, user store.UserProfile, entryID string, out *Result) error {
	char, err := store.LoadCharacter(ctx, tx, user.ID)
	if err != nil {
		return err
	}
	streak, _, err := store.LoadStreak(ctx, tx, user.ID)
	if err != nil {
		return err
	}
	loc := loadLocation(user.Timezone)
	localDate := s.db.Now().In(loc).Format("2006-01-02")
	totals, _, err := store.LoadDailyTotals(ctx, tx, user.ID, localDate)
	if err != nil {
		return err
	}
	*out = Result{
		EntryID:   entryID,
		LocalDate: localDate,
		DayTotals: totals,
		Goals:     user.Goals,
		Remaining: remaining(user.Goals, totals),
		Character: viewOf(char),
		Streak:    streak,
		Replayed:  true,
	}
	return nil
}

func viewOf(c domain.Character) CharacterView {
	return CharacterView{
		Level:    c.Level,
		XP:       c.XP,
		XPToNext: c.XPToNext(),
		CON:      c.CON(),
		VIT:      c.VIT(),
	}
}

func remaining(g domain.Goals, d domain.Macros) domain.Macros {
	return domain.Macros{
		KCal:     max(0, g.KCal-d.KCal),
		ProteinG: max(0, g.ProteinG-d.ProteinG),
		CarbsG:   max(0, g.CarbsG-d.CarbsG),
		FatG:     max(0, g.FatG-d.FatG),
	}
}

// loadLocation resolves the user's zone, falling back to UTC. Streaks are a
// local-calendar concept: a 9pm dinner in UTC-8 belongs to that user's today,
// not to UTC tomorrow, and getting this wrong silently breaks streaks for every
// user west of Greenwich.
func loadLocation(tz string) *time.Location {
	if tz == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

// daysBetween returns the whole-day gap between two YYYY-MM-DD local dates.
func daysBetween(prev, current string) int {
	if prev == "" {
		return 0
	}
	p, err1 := time.ParseInLocation("2006-01-02", prev, time.UTC)
	c, err2 := time.ParseInLocation("2006-01-02", current, time.UTC)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(c.Sub(p).Hours() / 24)
}

// AsRejected extracts a Path B error, if that is what err is.
func AsRejected(err error) (*domain.RejectedError, bool) {
	var re *domain.RejectedError
	if errors.As(err, &re) {
		return re, true
	}
	return nil, false
}
