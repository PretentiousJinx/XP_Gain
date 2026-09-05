package domain

import "testing"

func goals() Goals { return Goals{KCal: 2000, ProteinG: 150, CarbsG: 200, FatG: 65} }

func TestProgressionCountsOnlyTowardGoal(t *testing.T) {
	// Half the protein goal already logged; a 100g entry can only bank the 75g
	// that remains under the cap.
	before := Macros{KCal: 1000, ProteinG: 75}
	entry := Macros{KCal: 500, ProteinG: 100}

	p := ComputeProgression(before, entry, goals(), SourcePhoto, false)

	if p.CountedPro != 75 {
		t.Errorf("counted protein = %d, want 75", p.CountedPro)
	}
	if want := 75 * ConMicroPerProteinG; p.ConDelta != want {
		t.Errorf("con delta = %d, want %d", p.ConDelta, want)
	}
}

func TestProgressionIsNotFarmable(t *testing.T) {
	// Once the day is already past the goal, more food must not earn more CON.
	atGoal := Macros{KCal: 2000, ProteinG: 150}
	p := ComputeProgression(atGoal, Macros{KCal: 800, ProteinG: 60}, goals(), SourcePhoto, false)

	if p.ConDelta != 0 {
		t.Errorf("con delta past goal = %d, want 0", p.ConDelta)
	}
	if p.VitDelta >= 0 {
		t.Errorf("vit delta past the overshoot band = %d, want negative", p.VitDelta)
	}
}

func TestManualEntriesEarnReducedXP(t *testing.T) {
	before := Macros{}
	entry := Macros{KCal: 1000, ProteinG: 50}

	photo := ComputeProgression(before, entry, goals(), SourcePhoto, false)
	manual := ComputeProgression(before, entry, goals(), SourceManual, false)

	if manual.XPAwarded >= photo.XPAwarded {
		t.Errorf("manual XP %d should be below photo XP %d", manual.XPAwarded, photo.XPAwarded)
	}
	// Stats themselves are unaffected by pedigree: the food was still eaten.
	if manual.ConDelta != photo.ConDelta {
		t.Errorf("con delta should not depend on source: %d vs %d", manual.ConDelta, photo.ConDelta)
	}
}

func TestStreakTransitions(t *testing.T) {
	tests := []struct {
		name        string
		start       Streak
		days        int
		hadPrevious bool
		want        int
	}{
		{"first ever entry", Streak{}, 0, false, 1},
		{"same day again", Streak{Current: 3, Longest: 3}, 0, true, 3},
		{"next day", Streak{Current: 3, Longest: 3}, 1, true, 4},
		{"missed a day", Streak{Current: 9, Longest: 9}, 2, true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NextStreak(tt.start, tt.days, tt.hadPrevious)
			if got.Current != tt.want {
				t.Errorf("current = %d, want %d", got.Current, tt.want)
			}
			if got.Longest < got.Current {
				t.Errorf("longest %d must never trail current %d", got.Longest, got.Current)
			}
		})
	}
}

func TestBrokenStreakKeepsLongest(t *testing.T) {
	got := NextStreak(Streak{Current: 12, Longest: 12}, 5, true)
	if got.Current != 1 {
		t.Errorf("current = %d, want 1", got.Current)
	}
	if got.Longest != 12 {
		t.Errorf("longest = %d, want 12 preserved across the break", got.Longest)
	}
}

func TestApplyXPLevelsUp(t *testing.T) {
	c := Character{Level: 1, XP: 0}
	gained := c.ApplyXP(250) // 100 to reach L2, then 200 to reach L3
	if c.Level != 2 || gained != 1 {
		t.Errorf("level = %d (gained %d), want level 2 gaining 1", c.Level, gained)
	}
	if c.XP != 150 {
		t.Errorf("carried XP = %d, want 150", c.XP)
	}
}
