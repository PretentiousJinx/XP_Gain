package domain

// Tuning constants for the nutrition -> stat economy.
const (
	// Constitution accrues from protein actually counted toward the daily goal.
	ConMicroPerProteinG = 6

	// Vitality accrues from calories counted toward the daily goal.
	VitMicroPerKCal = 1

	// Calories beyond OvershootTolerance * goal drain Vitality.
	OvershootTolerance     = 1.15
	VitMicroPerOverKCal    = 2
	StatFloorMicro         = 1000 // no base stat may fall below 1.000
	XPPerCountedKCal       = 100  // divisor: 1 XP per 100 counted kcal
	XPManualPenaltyPct     = 50   // manual entries earn half XP
	XPFirstEntryOfDayBonus = 25
)

// Progression is the computed result of folding one entry into a day.
type Progression struct {
	DayBefore    Macros
	DayAfter     Macros
	CountedKCal  int // kcal that moved the user toward (not past) the goal
	CountedPro   int
	OverKCal     int
	ConDelta     int // micro-points
	VitDelta     int // micro-points
	XPAwarded    int
	LevelsGained int
}

// clampCounted returns the portion of an increase that moved a running total
// toward a cap without exceeding it. Progress past the goal earns nothing,
// which makes the economy monotone and non-farmable: logging 10,000 kcal of
// junk cannot out-earn logging exactly the goal.
func clampCounted(before, after, goal int) int {
	lo, hi := min(before, goal), min(after, goal)
	if d := hi - lo; d > 0 {
		return d
	}
	return 0
}

// ComputeProgression is pure: same inputs always yield the same stat movement.
// Keeping it free of DB and clock access is what lets the PvP state-check
// servers replay a user's entry log and independently verify a stat line.
func ComputeProgression(dayBefore Macros, entry Macros, goals Goals, src EntrySource, firstOfDay bool) Progression {
	p := Progression{DayBefore: dayBefore}
	p.DayAfter = dayBefore.Add(entry)

	p.CountedPro = clampCounted(dayBefore.ProteinG, p.DayAfter.ProteinG, goals.ProteinG)
	p.CountedKCal = clampCounted(dayBefore.KCal, p.DayAfter.KCal, goals.KCal)

	p.ConDelta = p.CountedPro * ConMicroPerProteinG
	p.VitDelta = p.CountedKCal * VitMicroPerKCal

	// Overshoot: only the newly-added calories past the tolerance band count.
	limit := int(float64(goals.KCal) * OvershootTolerance)
	overBefore, overAfter := 0, 0
	if dayBefore.KCal > limit {
		overBefore = dayBefore.KCal - limit
	}
	if p.DayAfter.KCal > limit {
		overAfter = p.DayAfter.KCal - limit
	}
	p.OverKCal = overAfter - overBefore
	p.VitDelta -= p.OverKCal * VitMicroPerOverKCal

	xp := p.CountedKCal / XPPerCountedKCal
	if firstOfDay {
		xp += XPFirstEntryOfDayBonus
	}
	if src.IsManual() {
		xp = xp * XPManualPenaltyPct / 100
	}
	p.XPAwarded = xp
	return p
}

// NextStreak folds a new activity date into a streak. Dates are local calendar
// dates in the user's zone (YYYY-MM-DD), never UTC: a user in UTC-8 logging
// dinner at 9pm must not have it counted as tomorrow.
//
// daysBetween is the caller-computed difference in local days; passing it in
// keeps this function pure and free of time-zone parsing.
func NextStreak(s Streak, daysBetween int, hadPrevious bool) Streak {
	switch {
	case !hadPrevious:
		s.Current = 1
	case daysBetween == 0:
		// Already logged today; streak is unchanged.
	case daysBetween == 1:
		s.Current++
	default:
		s.Current = 1
	}
	if s.Current > s.Longest {
		s.Longest = s.Current
	}
	return s
}
