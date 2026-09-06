// Package httpapi is the transport layer. It parses requests, delegates to the
// service, and maps results and errors onto HTTP. It contains no game rules.
package httpapi

import (
	"net/http"
	"time"

	"github.com/PretentiousJinx/xpgain/server/internal/service"
	"github.com/PretentiousJinx/xpgain/server/internal/vision"
)

// API wires the service and the token verifier into HTTP handlers.
type API struct {
	svc  *service.Service
	auth Authenticator
}

// New builds the API. The Authenticator is required; there is no unauthenticated
// mode, so a misconfigured deployment fails at startup rather than silently
// serving every request as an anonymous user.
func New(svc *service.Service, authenticator Authenticator) *API {
	return &API{svc: svc, auth: authenticator}
}

// Routes returns the mux. Go 1.22 method-aware patterns remove the need for a
// third-party router for a surface this small.
//
// Auth is applied per route rather than to the whole mux, so adding a route
// is an explicit decision about whether it is public. /healthz is the only
// public one.
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.health)
	mux.Handle("GET /v1/me", a.requireAuth(http.HandlerFunc(a.getMe)))
	mux.Handle("PUT /v1/me", a.requireAuth(http.HandlerFunc(a.putMe)))
	mux.Handle("POST /v1/intake/photo", a.requireAuth(http.HandlerFunc(a.postPhoto)))
	mux.Handle("POST /v1/intake/manual", a.requireAuth(http.HandlerFunc(a.postManual)))
	return withRecovery(withLogging(mux))
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// photoRequestBody is the wire shape for both a first attempt and a reattempt.
//
// Note the absence of any user field: identity comes from the verified token
// alone, so there is nothing here a client could set to act as someone else.
type photoRequestBody struct {
	ClientEntryID string         `json:"client_entry_id"`
	PhotoURI      string         `json:"photo_uri"`
	LoggedAt      *time.Time     `json:"logged_at,omitempty"`
	Payload       vision.Payload `json:"vision"`

	// Present only on a reattempt, echoing the rejection_id from the 422.
	SupersedesRejectionID string `json:"supersedes_rejection_id,omitempty"`
}

// postPhoto handles both the first photo submission and any later reattempt.
// They are the same operation with one extra field, so they share a route:
// a separate /reattempt endpoint would duplicate the whole validation path to
// express a single nullable link.
func (a *API) postPhoto(w http.ResponseWriter, r *http.Request) {
	userID, ok := UserIDFrom(r.Context())
	if !ok {
		unauthorized(w, "unauthorized", "Sign in required.")
		return
	}

	var body photoRequestBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, err)
		return
	}

	req := service.PhotoRequest{
		UserID:                userID,
		ClientEntryID:         body.ClientEntryID,
		PhotoURI:              body.PhotoURI,
		Payload:               body.Payload,
		SupersedesRejectionID: body.SupersedesRejectionID,
	}
	if body.LoggedAt != nil {
		req.LoggedAt = *body.LoggedAt
	}

	res, err := a.svc.SubmitPhoto(r.Context(), req)
	if err != nil {
		writeError(w, err) // includes the Path B 422 with validation_reasoning
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

// manualRequestBody is the override the user types after a rejection, or any
// time they choose to skip the camera.
type manualRequestBody struct {
	ClientEntryID string     `json:"client_entry_id"`
	KCal          int        `json:"kcal"`
	ProteinG      int        `json:"protein_g"`
	CarbsG        int        `json:"carbs_g"`
	FatG          int        `json:"fat_g"`
	LoggedAt      *time.Time `json:"logged_at,omitempty"`

	SupersedesRejectionID string `json:"supersedes_rejection_id,omitempty"`
}

// postManual records a user-entered override. The handler never lets the client
// choose its own pedigree: is_manual is derived from the route, not read from
// the body, so a client cannot submit typed numbers labelled as AI-verified.
func (a *API) postManual(w http.ResponseWriter, r *http.Request) {
	userID, ok := UserIDFrom(r.Context())
	if !ok {
		unauthorized(w, "unauthorized", "Sign in required.")
		return
	}

	var body manualRequestBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, err)
		return
	}

	req := service.ManualRequest{
		UserID:                userID,
		ClientEntryID:         body.ClientEntryID,
		Macros:                domainMacros(body.KCal, body.ProteinG, body.CarbsG, body.FatG),
		SupersedesRejectionID: body.SupersedesRejectionID,
	}
	if body.LoggedAt != nil {
		req.LoggedAt = *body.LoggedAt
	}

	res, err := a.svc.SubmitManual(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}
