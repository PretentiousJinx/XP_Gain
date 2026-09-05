// Package firebase talks to Google's REST APIs on behalf of the service
// account: Firestore for the auto-save sweep, and Identity Toolkit for token
// revocation checks.
package firebase

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Scopes requested for the service-account access token.
const (
	ScopeDatastore       = "https://www.googleapis.com/auth/datastore"
	ScopeIdentityToolkit = "https://www.googleapis.com/auth/identitytoolkit"
)

const defaultTokenURI = "https://oauth2.googleapis.com/token"

// ServiceAccount is the subset of a Google service-account JSON key we need.
type ServiceAccount struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
	TokenURI     string `json:"token_uri"`

	key *rsa.PrivateKey
}

// LoadServiceAccount reads and validates a service-account key file.
func LoadServiceAccount(path string) (*ServiceAccount, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read service account: %w", err)
	}
	return ParseServiceAccount(raw)
}

// ParseServiceAccount validates a service-account key and pre-parses its
// private key, so a malformed credential fails at startup rather than on the
// first sweep at 3am.
func ParseServiceAccount(raw []byte) (*ServiceAccount, error) {
	var sa ServiceAccount
	if err := json.Unmarshal(raw, &sa); err != nil {
		return nil, fmt.Errorf("parse service account: %w", err)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" || sa.ProjectID == "" {
		return nil, fmt.Errorf("service account is missing client_email, private_key or project_id")
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(sa.PrivateKey))
	if err != nil {
		return nil, fmt.Errorf("parse service account private key: %w", err)
	}
	sa.key = key
	if sa.TokenURI == "" {
		sa.TokenURI = defaultTokenURI
	}
	return &sa, nil
}

// TokenSource mints and caches OAuth2 access tokens for the service account.
//
// Safe for concurrent use. A refresh is collapsed under one mutex so a burst of
// sweeps or verifications arriving on an expired token produces one token
// exchange rather than one per caller.
type TokenSource struct {
	sa     *ServiceAccount
	scopes string
	client *http.Client
	now    func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewTokenSource builds a token source for the given scopes.
func NewTokenSource(sa *ServiceAccount, scopes ...string) *TokenSource {
	if len(scopes) == 0 {
		scopes = []string{ScopeDatastore, ScopeIdentityToolkit}
	}
	return &TokenSource{
		sa:     sa,
		scopes: strings.Join(scopes, " "),
		client: &http.Client{Timeout: 15 * time.Second},
		now:    time.Now,
	}
}

// refreshSkew renews a token slightly before it expires, so a request never
// leaves with a credential that dies in flight.
const refreshSkew = 60 * time.Second

// Token returns a valid access token, refreshing if necessary.
func (ts *TokenSource) Token(ctx context.Context) (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if ts.token != "" && ts.now().Before(ts.expires.Add(-refreshSkew)) {
		return ts.token, nil
	}

	assertion, err := ts.assertion()
	if err != nil {
		return "", err
	}

	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.sa.TokenURI,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := ts.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("exchange service account assertion: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %s", resp.Status)
	}

	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("token endpoint returned no access_token")
	}

	ts.token = out.AccessToken
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	ts.expires = ts.now().Add(ttl)
	return ts.token, nil
}

// assertion builds the signed JWT the token endpoint exchanges for an access
// token. It is short-lived by design: it never leaves this process except to
// Google, and a stolen assertion is only useful for its one-hour window.
func (ts *TokenSource) assertion() (string, error) {
	now := ts.now()
	claims := jwt.MapClaims{
		"iss":   ts.sa.ClientEmail,
		"scope": ts.scopes,
		"aud":   ts.sa.TokenURI,
		"exp":   now.Add(time.Hour).Unix(),
		"iat":   now.Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	if ts.sa.PrivateKeyID != "" {
		tok.Header["kid"] = ts.sa.PrivateKeyID
	}
	signed, err := tok.SignedString(ts.sa.key)
	if err != nil {
		return "", fmt.Errorf("sign service account assertion: %w", err)
	}
	return signed, nil
}

// StaticToken is a fixed token source for tests.
type StaticToken string

func (s StaticToken) Token(context.Context) (string, error) { return string(s), nil }

// Tokener is the credential dependency shared by the Firestore and Identity
// clients.
type Tokener interface {
	Token(ctx context.Context) (string, error)
}
