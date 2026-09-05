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
