package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/PretentiousJinx/xpgain/server/internal/domain"
	"github.com/PretentiousJinx/xpgain/server/internal/service"
)

// ErrorBody is the single error shape the mobile client parses.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`

	// Path B fields. The client renders ValidationReasoning verbatim and keeps
	// RejectionID so the follow-up manual entry or reattempt can reference it.
	RejectionID         string   `json:"rejection_id,omitempty"`
	ValidationReasoning string   `json:"validation_reasoning,omitempty"`
	Confidence          *float64 `json:"confidence,omitempty"`

	// Recovery affordances, sent so the client does not hard-code the workflow.
	CanRetryPhoto   bool `json:"can_retry_photo,omitempty"`
	CanEnterManual  bool `json:"can_enter_manual,omitempty"`
	NeedsOnboarding bool `json:"needs_onboarding,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encode response", "err", err)
	}
}

// writeError maps domain errors onto transport codes. This is the only place in
// the program that knows about HTTP status codes.
func writeError(w http.ResponseWriter, err error) {
	// Path B is not a server fault and not a 5xx. It is a well-formed,
	// expected answer that happens to be "no", so it returns 422 with the
	// model's reasoning attached rather than a generic failure.
	if rejected, ok := service.AsRejected(err); ok {
		body := ErrorBody{
			Code:                "photo_rejected",
			Message:             "The photo was not accepted as a food log.",
			RejectionID:         rejected.RejectionID,
			ValidationReasoning: rejected.ValidationReasoning,
			CanRetryPhoto:       true,
			CanEnterManual:      true,
		}
		if rejected.Confidence > 0 {
			c := rejected.Confidence
			body.Confidence = &c
		}
		writeJSON(w, http.StatusUnprocessableEntity, body)
		return
	}

	switch {
	case errors.Is(err, domain.ErrNotFound):
		// The overwhelmingly common cause is an authenticated user who has
		// never been provisioned. A generic "not found" leaves the client with
		// nothing to do; naming it tells the app to run onboarding.
		writeJSON(w, http.StatusNotFound, ErrorBody{
			Code:            "profile_not_found",
			Message:         "This account has not been set up yet.",
			NeedsOnboarding: true,
		})
	case errors.Is(err, domain.ErrInvalidPayload):
		writeJSON(w, http.StatusBadRequest, ErrorBody{Code: "invalid_payload", Message: err.Error()})
	case errors.Is(err, domain.ErrRejectionClosed):
		writeJSON(w, http.StatusConflict, ErrorBody{
			Code:    "rejection_already_resolved",
			Message: "That rejection has already been answered by another entry.",
		})
	default:
		// Never leak internal error text to the client; log it instead.
		slog.Error("unhandled request failure", "err", err)
		writeJSON(w, http.StatusInternalServerError, ErrorBody{
			Code: "internal_error", Message: "Something went wrong. Please try again.",
		})
	}
}

// decodeJSON reads a request body with a size cap and strict field checking.
// DisallowUnknownFields turns a client/server contract drift into a loud 400
// during development rather than a silently ignored field in production.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	const maxBody = 1 << 20 // 1 MiB
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return domain.ErrInvalidPayload
	}
	return nil
}
