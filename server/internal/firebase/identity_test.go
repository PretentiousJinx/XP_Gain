package firebase

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PretentiousJinx/xpgain/server/internal/auth"
)

var idNow = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

func newIdentityTo(t *testing.T, h http.HandlerFunc) *Identity {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	id := NewIdentity("test-project", StaticToken("access-token"))
	id.Host = srv.URL
	id.now = func() time.Time { return idNow }
	return id
}

// accountsResponse renders an Identity Toolkit lookup body.
func accountsResponse(validSince int64, disabled bool) string {
	return fmt.Sprintf(
		`{"users":[{"localId":"u1","validSince":"%d","disabled":%t}]}`, validSince, disabled)
}

func TestTokenIssuedBeforeRevocationIsRejected(t *testing.T) {
	revokedAt := idNow.Add(-10 * time.Minute)
	id := newIdentityTo(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(accountsResponse(revokedAt.Unix(), false)))
	})

	// Signed in an hour ago, but the session was revoked ten minutes ago.
	authTime := idNow.Add(-time.Hour)

	err := id.CheckRevoked(context.Background(), "u1", authTime)
	if err == nil {
		t.Fatal("a token predating the revocation watermark was accepted")
	}
	if !errors.Is(err, auth.ErrRevoked) {
		t.Errorf("err = %v, want ErrRevoked", err)
	}
}

func TestTokenIssuedAfterRevocationIsAccepted(t *testing.T) {
	revokedAt := idNow.Add(-time.Hour)
	id := newIdentityTo(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(accountsResponse(revokedAt.Unix(), false)))
	})

	// Signed in again after the revocation, so this session is legitimate.
	authTime := idNow.Add(-5 * time.Minute)

	if err := id.CheckRevoked(context.Background(), "u1", authTime); err != nil {
		t.Errorf("a session created after the revocation should pass: %v", err)
	}
}

func TestDisabledAccountIsRejected(t *testing.T) {
	id := newIdentityTo(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(accountsResponse(0, true)))
	})

	err := id.CheckRevoked(context.Background(), "u1", idNow.Add(-time.Minute))
	if !errors.Is(err, auth.ErrRevoked) {
		t.Errorf("err = %v, want ErrRevoked for a disabled account", err)
	}
}

func TestDeletedAccountIsRejected(t *testing.T) {
	id := newIdentityTo(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"users":[]}`))
	})

	if err := id.CheckRevoked(context.Background(), "u1", idNow.Add(-time.Minute)); err == nil {
		t.Fatal("a token for a deleted account was accepted")
	}
}

func TestLookupIsCachedWithinTTL(t *testing.T) {
	var calls int32
	id := newIdentityTo(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Write([]byte(accountsResponse(0, false)))
	})
	id.TTL = time.Minute

	for i := 0; i < 5; i++ {
		if err := id.CheckRevoked(context.Background(), "u1", idNow.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("%d upstream lookups for 5 checks, want 1", got)
	}
}

func TestCacheExpiresAfterTTL(t *testing.T) {
	var calls int32
	id := newIdentityTo(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Write([]byte(accountsResponse(0, false)))
	})
	id.TTL = time.Minute

	now := idNow
	id.now = func() time.Time { return now }

	id.CheckRevoked(context.Background(), "u1", idNow.Add(-time.Minute))
	now = now.Add(2 * time.Minute) // past the TTL
	id.CheckRevoked(context.Background(), "u1", idNow.Add(-time.Minute))

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("%d lookups, want 2 once the TTL lapsed", got)
	}
}

func TestOutageServesStaleStateRatherThanLockingEveryoneOut(t *testing.T) {
	var fail atomic.Bool
	var calls int32
	id := newIdentityTo(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(accountsResponse(0, false)))
	})
	id.TTL = time.Minute

	now := idNow
	id.now = func() time.Time { return now }

	// Warm the cache, then break the upstream and step past the TTL.
	if err := id.CheckRevoked(context.Background(), "u1", idNow.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	now = now.Add(2 * time.Minute)

	if err := id.CheckRevoked(context.Background(), "u1", idNow.Add(-time.Minute)); err != nil {
		t.Errorf("a known user should survive an Identity Toolkit outage: %v", err)
	}
}

func TestUnknownUserDuringOutageIsRefused(t *testing.T) {
	// With no cached state there is nothing to fall back on, and guessing
	// "probably fine" would let a revoked session through during an outage.
	id := newIdentityTo(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	if err := id.CheckRevoked(context.Background(), "never-seen", idNow); err == nil {
		t.Fatal("an unverifiable account must not be accepted")
	}
}

func TestMissingAuthTimeIsRefused(t *testing.T) {
	id := newIdentityTo(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(accountsResponse(idNow.Unix(), false)))
	})

	if err := id.CheckRevoked(context.Background(), "u1", time.Time{}); err == nil {
		t.Fatal("without auth_time there is nothing to compare; it must not pass")
	}
}

func TestLookupSendsCredentialsAndUID(t *testing.T) {
	var authHeader string
	var body map[string]any
	id := newIdentityTo(t, func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(accountsResponse(0, false)))
	})

	id.CheckRevoked(context.Background(), "u1", idNow.Add(-time.Minute))

	if authHeader != "Bearer access-token" {
		t.Errorf("Authorization = %q", authHeader)
	}
	ids, _ := body["localId"].([]any)
	if len(ids) != 1 || ids[0] != "u1" {
		t.Errorf("localId = %#v, want [u1]", body["localId"])
	}
}

// --- service account / token source ---------------------------------------

func serviceAccountJSON(t *testing.T, tokenURI string) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})

	raw, _ := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "test-project",
		"private_key_id": "pk1",
		"private_key":    string(keyPEM),
		"client_email":   "sweeper@test-project.iam.gserviceaccount.com",
		"token_uri":      tokenURI,
	})
	return raw
}

func TestMalformedServiceAccountIsRejectedUpFront(t *testing.T) {
	for _, bad := range []string{
		`{}`,
		`{"client_email":"a@b.com"}`,
		`{"client_email":"a@b.com","project_id":"p","private_key":"not a pem"}`,
		`not json`,
	} {
		if _, err := ParseServiceAccount([]byte(bad)); err == nil {
			t.Errorf("accepted a malformed service account: %s", bad)
		}
	}
}

func TestTokenSourceExchangesAndCaches(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if got := r.PostForm.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			t.Errorf("grant_type = %q", got)
		}
		if r.PostForm.Get("assertion") == "" {
			t.Error("no signed assertion was sent")
		}
		w.Write([]byte(`{"access_token":"at-1","expires_in":3600}`))
	}))
	defer srv.Close()

	sa, err := ParseServiceAccount(serviceAccountJSON(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	ts := NewTokenSource(sa)

	for i := 0; i < 3; i++ {
		tok, err := ts.Token(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if tok != "at-1" {
			t.Errorf("token = %q", tok)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("%d token exchanges for 3 calls, want 1 (cached)", got)
	}
}

func TestTokenIsRefreshedBeforeExpiry(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		fmt.Fprintf(w, `{"access_token":"at-%d","expires_in":120}`, n)
	}))
	defer srv.Close()

	sa, _ := ParseServiceAccount(serviceAccountJSON(t, srv.URL))
	ts := NewTokenSource(sa)

	now := idNow
	ts.now = func() time.Time { return now }

	first, _ := ts.Token(context.Background())
	// Step inside the refresh skew: the token is still nominally valid but
	// would die in flight, so it must be renewed now rather than then.
	now = now.Add(90 * time.Second)
	second, _ := ts.Token(context.Background())

	if first == second {
		t.Error("token was not refreshed inside the skew window")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("%d exchanges, want 2", got)
	}
}

func TestTokenEndpointFailureSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	sa, _ := ParseServiceAccount(serviceAccountJSON(t, srv.URL))
	if _, err := NewTokenSource(sa).Token(context.Background()); err == nil {
		t.Fatal("a rejected assertion must surface as an error")
	}
}
