package domain

// RejectionRecord is the persisted form of a Path B outcome. Storing rejections
// rather than only returning them gives the workflow a resumable handle: the
// client comes back with this ID when it retries a photo or types values in by
// hand, and the resulting entry is permanently linked to the refusal it answered.
type RejectionRecord struct {
	ID                  string   `json:"id"`
	UserID              string   `json:"user_id"`
	ClientEntryID       string   `json:"client_entry_id"`
	ValidationReasoning string   `json:"validation_reasoning"`
	AIConfidence        *float64 `json:"ai_confidence,omitempty"`
	AIModel             string   `json:"ai_model,omitempty"`
	PhotoURI            string   `json:"photo_uri,omitempty"`
	ResolvedByEntryID   string   `json:"resolved_by_entry_id,omitempty"`
}

// IsResolved reports whether a later entry already answered this rejection.
func (r RejectionRecord) IsResolved() bool { return r.ResolvedByEntryID != "" }
