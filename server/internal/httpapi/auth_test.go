package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PretentiousJinx/xpgain/server/internal/auth"
	"github.com/PretentiousJinx/xpgain/server/internal/service"
	"github.com/PretentiousJinx/xpgain/server/internal/store"
)

const authedUID = "firebase-uid-real"

// fakeAuth stands in for the Firebase verifier. The verifier's own correctness
// is covered in internal/auth; what matters here is that the transport layer
// consults it at all, and honours its answer.
type fakeAuth struct {
	token *auth.Token
	err   error
	calls int
}

func (f *fakeAuth) Verify(_ context.Context, raw string) (*auth.Token, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if raw != "good-token" {
		return nil, auth.ErrInvalid
	}
	return f.token, nil
}

func okAuth() *fakeAuth {
	return &fakeAuth{token: &auth.Token{UID: authedUID}}
}

// --- middleware ------------------------------------------------------------

func TestRequireAuthRejectsMissingAndMalformedCredentials(t *testing.T) {
	tests := []struct {
		name   string
		header string
	}{
		{"no header", ""},
		{"empty bearer", "Bearer "},
		{"bearer with no token", "Bearer"},
		{"wrong scheme", "Basic dXNlcjpwYXNz"},
		{"raw token, no scheme", "good-token"},
		{"scheme glued to token", "Bearergood-token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fa := okAuth()
			api := &API{auth: fa}

			reached := false
			h := api.requireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				reached = true
			}))

			req := httptest.NewRequest(http.MethodPost, "/v1/intake/manual", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if reached {
				t.Error("an unauthenticated request reached the handler")
			}
			if got := rec.Header().Get("WWW-Authenticate"); got == "" {
				t.Error("401 should carry a WWW-Authenticate header")
			}
		})
	}
}

func TestRequireAuthAcceptsValidTokenAndPassesUID(t *testing.T) {
	api := &API{auth: okAuth()}

	var seen string
	h := api.requireAuth(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, _ = UserIDFrom(r.Context())
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/intake/manual", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen != authedUID {
		t.Errorf("handler saw UID %q, want %q", seen, authedUID)
	}
}

func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	for _, scheme := range []string{"Bearer", "bearer", "BEARER", "BeArEr"} {
		api := &API{auth: okAuth()}
		reached := false
		h := api.requireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			reached = true
		}))

		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		req.Header.Set("Authorization", scheme+" good-token")
		h.ServeHTTP(httptest.NewRecorder(), req)

		if !reached {
			t.Errorf("scheme %q was not accepted; RFC 7235 makes it case-insensitive", scheme)
		}
	}
}

func TestExpiredTokenReportsRefreshableCode(t *testing.T) {
	api := &API{auth: &fakeAuth{err: fmt.Errorf("%w: stale", auth.ErrExpired)}}

	h := api.requireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("expired token reached the handler")
	}))

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Authorization", "Bearer whatever")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var body ErrorBody
	json.NewDecoder(rec.Body).Decode(&body)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	// The client keys off this to refresh rather than bounce to a sign-in screen.
	if body.Code != "token_expired" {
		t.Errorf("code = %q, want token_expired", body.Code)
	}
}

func TestRejectionReasonIsNotLeakedToClient(t *testing.T) {
	api := &API{auth: &fakeAuth{
		err: fmt.Errorf("%w: signature mismatch on kid abc123", auth.ErrInvalid),
	}}

	h := api.requireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Authorization", "Bearer forged")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "kid abc123") ||
		strings.Contains(rec.Body.String(), "signature") {
		t.Errorf("internal failure detail leaked to the client: %s", rec.Body.String())
	}
}

func TestUserIDCannotBeForgedViaContext(t *testing.T) {
	// The context key is a private type, so a caller outside this package
	// cannot plant a UID. Planting one under a same-shaped key must not work.
	type impostorKey struct{}
	ctx := context.WithValue(context.Background(), impostorKey{}, "admin")

	if uid, ok := UserIDFrom(ctx); ok {
		t.Errorf("UserIDFrom honoured a foreign context key, returning %q", uid)
	}
}

// --- routing ---------------------------------------------------------------

func TestProtectedRoutesRequireAuthAndHealthDoesNot(t *testing.T) {
	fa := okAuth()
	api := New(nil, fa) // nil service is fine: no handler should be reached

	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	for _, path := range []string{"/v1/intake/photo", "/v1/intake/manual"} {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without a token = %d, want 401", path, resp.StatusCode)
		}
	}

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz = %d, want 200 without auth", resp.StatusCode)
	}
}

// TestEveryProtectedRouteConsultsTheVerifier proves the middleware is actually
// in each route's chain.
//
// The status-code test above cannot show this: the handlers independently
// refuse a request with no UID in context, so an unwrapped route still answers
// 401 and the test passes while the gate is missing. Asserting the verifier was
// *called* is what distinguishes a wired middleware from a lucky status code.
func TestEveryProtectedRouteConsultsTheVerifier(t *testing.T) {
	db, uid := seededDB(t)
	protected := map[string]string{
		"/v1/intake/manual": `{"client_entry_id":"m1","kcal":100,"protein_g":5,"carbs_g":5,"fat_g":5}`,
		"/v1/intake/photo": `{"client_entry_id":"p1","photo_uri":"file://x.jpg",
			"vision":{"is_valid_food":true,"kcal":100,"protein_g":5,"carbs_g":5,"fat_g":5,
			          "confidence":0.9,"model":"m","validation_reasoning":""}}`,
	}

	for path, body := range protected {
		t.Run(path, func(t *testing.T) {
			fa := okAuth()
			srv := httptest.NewServer(New(service.New(db), fa).Routes())
			defer srv.Close()

			req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer good-token")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if fa.calls != 1 {
				t.Fatalf("verifier called %d times for %s; the route is not behind requireAuth",
					fa.calls, path)
			}
			if resp.StatusCode == http.StatusUnauthorized {
				t.Errorf("a valid token was rejected on %s", path)
			}
		})
	}
	_ = uid
}

// seededDB returns a store with one provisioned user and character.
func seededDB(t *testing.T) (*store.DB, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	now := db.Now().Format(time.RFC3339Nano)
	err = db.WithTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users
			(id, timezone, goal_kcal, goal_protein_g, goal_carbs_g, goal_fat_g, created_at, updated_at, is_synced)
			VALUES (?,'UTC',2000,150,200,65,?,?,1)`, authedUID, now, now); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO characters
			(user_id, level, xp, con_micro, vit_micro, updated_at, is_synced)
			VALUES (?,1,0,5000,5000,?,1)`, authedUID, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, authedUID
}

// --- identity binding, end to end -----------------------------------------

func TestEntryIsAttributedToTheTokenNotTheBody(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := db.Now().Format(time.RFC3339Nano)
	seed := func(uid string) {
		err := db.WithTx(context.Background(), func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT INTO users
				(id, timezone, goal_kcal, goal_protein_g, goal_carbs_g, goal_fat_g, created_at, updated_at, is_synced)
				VALUES (?,'UTC',2000,150,200,65,?,?,1)`, uid, now, now); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO characters
				(user_id, level, xp, con_micro, vit_micro, updated_at, is_synced)
				VALUES (?,1,0,5000,5000,?,1)`, uid, now)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	seed(authedUID)
	seed("victim-uid")

	api := New(service.New(db), okAuth())
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	// The body names a different user. Identity must come from the token.
	body := `{"client_entry_id":"c1","kcal":500,"protein_g":30,"carbs_g":40,"fat_g":10,
	          "user_id":"victim-uid"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/intake/manual", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer good-token")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// DisallowUnknownFields means an attempt to smuggle user_id is a hard 400
	// rather than a silently ignored field.
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unrecognised body field", resp.StatusCode)
	}

	var victimRows int
	db.Reader().QueryRow(
		`SELECT COUNT(*) FROM macro_entries WHERE user_id = ?`, "victim-uid").Scan(&victimRows)
	if victimRows != 0 {
		t.Error("an entry was written against a user named only in the request body")
	}
}

func TestAuthenticatedEntryLandsUnderTheTokenUID(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := db.Now().Format(time.RFC3339Nano)
	err = db.WithTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users
			(id, timezone, goal_kcal, goal_protein_g, goal_carbs_g, goal_fat_g, created_at, updated_at, is_synced)
			VALUES (?,'UTC',2000,150,200,65,?,?,1)`, authedUID, now, now); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO characters
			(user_id, level, xp, con_micro, vit_micro, updated_at, is_synced)
			VALUES (?,1,0,5000,5000,?,1)`, authedUID, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	api := New(service.New(db), okAuth())
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	body := `{"client_entry_id":"c1","kcal":500,"protein_g":30,"carbs_g":40,"fat_g":10}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/intake/manual", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer good-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	var owner string
	if err := db.Reader().QueryRow(
		`SELECT user_id FROM macro_entries LIMIT 1`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != authedUID {
		t.Errorf("entry owner = %q, want the token UID %q", owner, authedUID)
	}
}

// --- the full first-run journey -------------------------------------------

// TestNewUserCanOnboardAndLogFood walks the path a brand-new install takes.
// Until provisioning existed this was impossible: an authenticated user had
// nowhere to exist, so every intake returned 404.
func TestNewUserCanOnboardAndLogFood(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "e2e.db"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	srv := httptest.NewServer(New(service.New(db), okAuth()).Routes())
	defer srv.Close()

	do := func(method, path, body string) (*http.Response, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer good-token")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		return resp, out
	}

	// 1. Fresh account: the client is told to onboard, not just "not found".
	resp, body := do(http.MethodGet, "/v1/me", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /v1/me on a fresh account = %d, want 404", resp.StatusCode)
	}
	if body["code"] != "profile_not_found" || body["needs_onboarding"] != true {
		t.Errorf("404 body does not tell the client to onboard: %v", body)
	}

	// 2. Provision.
	resp, body = do(http.MethodPut, "/v1/me",
		`{"timezone":"America/Los_Angeles","goal_kcal":2200,"goal_protein_g":160,
		  "goal_carbs_g":220,"goal_fat_g":70}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT /v1/me = %d, want 201", resp.StatusCode)
	}
	if body["created"] != true {
		t.Error("first PUT should report created")
	}

	// 3. A second PUT is a settings update, not a conflict.
	resp, body = do(http.MethodPut, "/v1/me",
		`{"timezone":"America/Los_Angeles","goal_kcal":2400,"goal_protein_g":160,
		  "goal_carbs_g":220,"goal_fat_g":70}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second PUT /v1/me = %d, want 200", resp.StatusCode)
	}
	if body["created"] != false {
		t.Error("second PUT should not report created")
	}

	// 4. Log a meal.
	resp, _ = do(http.MethodPost, "/v1/intake/manual",
		`{"client_entry_id":"e1","kcal":700,"protein_g":50,"carbs_g":60,"fat_g":20}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST intake = %d, want 201", resp.StatusCode)
	}

	// 5. The account view reflects it.
	resp, body = do(http.MethodGet, "/v1/me", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/me = %d, want 200", resp.StatusCode)
	}
	totals, _ := body["day_totals"].(map[string]any)
	if totals["kcal"] != float64(700) {
		t.Errorf("day totals = %v, want 700 kcal", totals["kcal"])
	}
	streak, _ := body["streak"].(map[string]any)
	if streak["current_streak"] != float64(1) {
		t.Errorf("streak = %v, want 1", streak["current_streak"])
	}
}

func TestProfileRoutesRequireAuth(t *testing.T) {
	srv := httptest.NewServer(New(nil, okAuth()).Routes())
	defer srv.Close()

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/me"},
		{http.MethodPut, "/v1/me"},
	} {
		req, _ := http.NewRequest(tc.method, srv.URL+tc.path, strings.NewReader(`{}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without a token = %d, want 401", tc.method, tc.path, resp.StatusCode)
		}
	}
}

func TestGoalsCannotBeSetToTrivialValuesOverHTTP(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "g.db"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	srv := httptest.NewServer(New(service.New(db), okAuth()).Routes())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/v1/me",
		strings.NewReader(`{"timezone":"UTC","goal_kcal":1,"goal_protein_g":1,"goal_carbs_g":1,"goal_fat_g":1}`))
	req.Header.Set("Authorization", "Bearer good-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a 1 kcal goal was accepted (%d); it would make any snack on-target", resp.StatusCode)
	}
}
