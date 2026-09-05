package domain

import "time"

// MicroPerPoint is the fixed-point scale for base stats. Stats accrue in
// thousandths so a single meal can move a stat fractionally without floats.
const MicroPerPoint = 1000

// Character holds the RPG state derived from nutrition adherence.
type Character struct {
	UserID   string `json:"user_id"`
	Level    int    `json:"level"`
	XP       int    `json:"xp"`
	ConMicro int    `json:"-"` // Constitution, fixed-point
	VitMicro int    `json:"-"` // Vitality, fixed-point
}

// CON and VIT expose the whole-number stat the UI and PvP layer read.
func (c Character) CON() int { return c.ConMicro / MicroPerPoint }
func (c Character) VIT() int { return c.VitMicro / MicroPerPoint }

// XPToNext keeps the original curve from the prototype: level * 100.
func (c Character) XPToNext() int { return c.Level * 100 }

// ApplyXP folds in XP and resolves any level-ups, returning levels gained.
func (c *Character) ApplyXP(amount int) int {
	if amount <= 0 {
		return 0
	}
	c.XP += amount
	gained := 0
	for c.XP >= c.XPToNext() {
		c.XP -= c.XPToNext()
		c.Level++
		gained++
	}
	return gained
}

// Streak tracks consecutive local days with at least one logged entry.
type Streak struct {
	UserID         string     `json:"user_id"`
	Current        int        `json:"current_streak"`
	Longest        int        `json:"longest_streak"`
	LastLocalDate  string     `json:"last_local_date"`
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
}
