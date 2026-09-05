package domain

import "time"

// Macros is a nutrition vector. Grams and kcal are integers on purpose: this is
// a game economy, and float drift across millions of accumulate/compare cycles
// would desynchronise client and server totals.
type Macros struct {
	KCal     int `json:"kcal"`
	ProteinG int `json:"protein_g"`
	CarbsG   int `json:"carbs_g"`
	FatG     int `json:"fat_g"`
}

func (m Macros) Add(o Macros) Macros {
	return Macros{m.KCal + o.KCal, m.ProteinG + o.ProteinG, m.CarbsG + o.CarbsG, m.FatG + o.FatG}
}

// Valid rejects negatives and physiologically absurd single entries. The upper
// bounds stop a malformed AI parse or a hostile client from farming stats.
func (m Macros) Valid() bool {
	if m.KCal < 0 || m.ProteinG < 0 || m.CarbsG < 0 || m.FatG < 0 {
		return false
	}
	return m.KCal <= 20000 && m.ProteinG <= 2000 && m.CarbsG <= 2000 && m.FatG <= 2000
}

// Goals is the user's daily macro target.
type Goals struct {
	KCal     int `json:"goal_kcal"`
	ProteinG int `json:"goal_protein_g"`
	CarbsG   int `json:"goal_carbs_g"`
	FatG     int `json:"goal_fat_g"`
}

// EntrySource records data pedigree. PvP state-check servers read this to decide
// how much to trust a character's stat line.
type EntrySource string

const (
	SourcePhoto     EntrySource = "photo"     // AI-validated photo, first attempt
	SourceReattempt EntrySource = "reattempt" // AI-validated photo, after a rejection
	SourceManual    EntrySource = "manual"    // user-typed, unverified
)

// IsManual is the denormalised pedigree flag persisted as is_manual.
func (s EntrySource) IsManual() bool { return s == SourceManual }

// MacroEntry is one logged food item.
type MacroEntry struct {
	ID            string      `json:"id"`
	UserID        string      `json:"user_id"`
	ClientEntryID string      `json:"client_entry_id"`
	LocalDate     string      `json:"local_date"` // YYYY-MM-DD in the user's zone
	Macros        Macros      `json:"macros"`
	Source        EntrySource `json:"source"`
	IsManual      bool        `json:"is_manual"`
	AIConfidence  *float64    `json:"ai_confidence,omitempty"`
	AIModel       string      `json:"ai_model,omitempty"`
	PhotoURI      string      `json:"photo_uri,omitempty"`
	SupersedesID  string      `json:"supersedes_rejection_id,omitempty"`
	LoggedAt      time.Time   `json:"logged_at"`
}
