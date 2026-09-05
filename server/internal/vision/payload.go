// Package vision defines the contract for parsed Vision AI output.
//
// This package deliberately contains no logic beyond validation. It is the
// quarantine boundary: everything here arrives from a model and is therefore
// untrusted input, no matter how well-formed it looks. Nothing downstream may
// accept a raw vision payload, only the validated value objects it yields.
package vision

import (
	"fmt"
	"strings"

	"github.com/PretentiousJinx/xpgain/server/internal/domain"
)

// Payload is the parsed JSON handed back by the Vision AI endpoint.
type Payload struct {
	// IsValidFood is the routing flag. Path A when true, Path B when false.
	IsValidFood bool `json:"is_valid_food"`

	// ValidationReasoning is the model's explanation. Required when
	// IsValidFood is false, because it is the entire content of the Path B
	// response the user reads.
	ValidationReasoning string `json:"validation_reasoning"`

	// Estimated macros. Only meaningful when IsValidFood is true.
	KCal     int `json:"kcal"`
	ProteinG int `json:"protein_g"`
	CarbsG   int `json:"carbs_g"`
	FatG     int `json:"fat_g"`

	Confidence  float64 `json:"confidence"`
	Model       string  `json:"model"`
	DetectedFood string `json:"detected_food,omitempty"`
}

// MinConfidence is the floor below which a nominally valid parse is treated as
// a rejection. A model that says "this is food" at 20% confidence is guessing,
// and a guess that silently moves a player's base stats is worse than an
// honest refusal the player can correct.
const MinConfidence = 0.55

// Macros projects the payload onto the domain value object.
func (p Payload) Macros() domain.Macros {
	return domain.Macros{KCal: p.KCal, ProteinG: p.ProteinG, CarbsG: p.CarbsG, FatG: p.FatG}
}

// Reasoning returns a non-empty, trimmed explanation, falling back to a generic
// message so the client never renders an empty rejection dialog.
func (p Payload) Reasoning() string {
	if s := strings.TrimSpace(p.ValidationReasoning); s != "" {
		return s
	}
	return "The photo could not be identified as food. Try a clearer, closer shot of the meal."
}

// Accepted reports whether the payload should take Path A, and if not, why.
//
// Three things can send a payload down Path B, and they are collapsed here on
// purpose so the service layer has exactly one branch to write:
//   - the model set is_valid_food to false;
//   - the model claimed food but below the confidence floor;
//   - the model claimed food but returned macros no meal could have.
func (p Payload) Accepted() (bool, string) {
	if !p.IsValidFood {
		return false, p.Reasoning()
	}
	if p.Confidence < MinConfidence {
		return false, fmt.Sprintf(
			"The photo looked like food, but identification confidence was only %.0f%%. "+
				"Retake the photo or enter the macros manually.", p.Confidence*100)
	}
	if !p.Macros().Valid() {
		return false, "The estimated nutrition values were outside a plausible range for a single meal. " +
			"Retake the photo or enter the macros manually."
	}
	return true, ""
}
