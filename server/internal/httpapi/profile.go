package httpapi

import (
	"net/http"

	"github.com/PretentiousJinx/xpgain/server/internal/service"
)

// profileBody is the provisioning payload.
//
// As with the intake routes, there is no user field: the account acted on is
// always the one the verified token names.
type profileBody struct {
	Timezone string `json:"timezone"`
	KCal     int    `json:"goal_kcal"`
	ProteinG int    `json:"goal_protein_g"`
	CarbsG   int    `json:"goal_carbs_g"`
	FatG     int    `json:"goal_fat_g"`
}

// putMe provisions the signed-in user, or updates their goals.
//
// PUT rather than POST because it is idempotent: the client calls it
// unconditionally at launch without having to know whether this account has
// ever been seen before. First call returns 201, later ones 200.
func (a *API) putMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := UserIDFrom(r.Context())
	if !ok {
		unauthorized(w, "unauthorized", "Sign in required.")
		return
	}

	var body profileBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, err)
		return
	}

	res, err := a.svc.EnsureProfile(r.Context(), service.ProfileRequest{
		UserID:   userID,
		Timezone: body.Timezone,
		Goals:    domainGoals(body.KCal, body.ProteinG, body.CarbsG, body.FatG),
	})
	if err != nil {
		writeError(w, err)
		return
	}

	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, res)
}

// getMe returns the account state the client renders on launch.
func (a *API) getMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := UserIDFrom(r.Context())
	if !ok {
		unauthorized(w, "unauthorized", "Sign in required.")
		return
	}

	res, err := a.svc.GetProfile(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
