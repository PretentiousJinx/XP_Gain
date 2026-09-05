package firebase

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/PretentiousJinx/xpgain/server/internal/auth"
)

// DefaultIdentityHost is Google's Identity Toolkit endpoint.
const DefaultIdentityHost = "https://identitytoolkit.googleapis.com"

// accountState is what the revocation check needs from an account record.
type accountState struct {
	validSince time.Time
	disabled   bool
	fetchedAt  time.Time
}

// Identity answers revocation questions against Firebase Auth.
//
// Signature verification alone cannot detect revocation: an ID token stays
// cryptographically valid until it expires, so a session revoked or a password
// changed five minutes ago is still honoured for the rest of the token's hour.
// This closes that window by consulting the account's validSince watermark.
type Identity struct {
	ProjectID string
	Host      string
	Token     Tokener
	Client    *http.Client

	// TTL bounds how long an account record is reused. It is the revocation
	// latency: a revoked session keeps working for at most this long, traded
	// against one upstream call per user per TTL.
	TTL time.Duration

	now func() time.Time

	mu    sync.RWMutex
	cache map[string]accountState
}

// NewIdentity builds a revocation checker.
func NewIdentity(projectID string, token Tokener) *Identity {
	return &Identity{
		ProjectID: projectID,
		Host:      DefaultIdentityHost,
		Token:     token,
		Client:    &http.Client{Timeout: 15 * time.Second},
		TTL:       60 * time.Second,
		now:       time.Now,
		cache:     make(map[string]accountState),
	}
}

func (i *Identity) clock() time.Time {
	if i.now != nil {
		return i.now()
	}
	return time.Now()
}

// CheckRevoked implements auth.RevocationChecker.
//
// A token is revoked when it was issued before the account's validSince
// watermark, which Firebase advances on sign-out-everywhere, password change,
// and explicit session revocation.
func (i *Identity) CheckRevoked(ctx context.Context, uid string, authTime time.Time) error {
	state, err := i.lookup(ctx, uid)
	if err != nil {
		return err
	}
	if state.disabled {
		return fmt.Errorf("%w: account disabled", auth.ErrRevoked)
	}
	if authTime.IsZero() {
		// Without auth_time there is nothing to compare against; treat it as
		// unverifiable rather than silently accepting.
		return fmt.Errorf("%w: token has no auth_time to check against", auth.ErrInvalid)
	}
	if !state.validSince.IsZero() && authTime.Before(state.validSince) {
		return fmt.Errorf("%w: session revoked at %s", auth.ErrRevoked,
			state.validSince.UTC().Format(time.RFC3339))
	}
	return nil
}

func (i *Identity) lookup(ctx context.Context, uid string) (accountState, error) {
	ttl := i.TTL
	if ttl <= 0 {
		ttl = 60 * time.Second
	}

	i.mu.RLock()
	cached, ok := i.cache[uid]
	i.mu.RUnlock()
	if ok && i.clock().Sub(cached.fetchedAt) < ttl {
		return cached, nil
	}

	state, err := i.fetch(ctx, uid)
	if err != nil {
		// Serve a stale record rather than locking every user out during a
		// transient Identity Toolkit outage. The staleness is bounded by how
		// long the outage lasts, and the token's own exp still applies.
		if ok {
			return cached, nil
		}
		return accountState{}, fmt.Errorf("%w: cannot verify revocation: %v", auth.ErrInvalid, err)
	}

	i.mu.Lock()
	i.cache[uid] = state
	i.mu.Unlock()
	return state, nil
}

func (i *Identity) fetch(ctx context.Context, uid string) (accountState, error) {
	token, err := i.Token.Token(ctx)
	if err != nil {
		return accountState{}, fmt.Errorf("access token: %w", err)
	}

	host := i.Host
	if host == "" {
		host = DefaultIdentityHost
	}
	url := fmt.Sprintf("%s/v1/projects/%s/accounts:lookup", host, i.ProjectID)

	body, _ := json.Marshal(map[string]any{"localId": []string{uid}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return accountState{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := i.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return accountState{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return accountState{}, fmt.Errorf("accounts:lookup returned %s: %s",
			resp.Status, bytes.TrimSpace(snippet))
	}

	var out struct {
		Users []struct {
			LocalID    string `json:"localId"`
			ValidSince string `json:"validSince"`
			Disabled   bool   `json:"disabled"`
		} `json:"users"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return accountState{}, fmt.Errorf("decode accounts:lookup: %w", err)
	}
	if len(out.Users) == 0 {
		// The account is gone. A token for a deleted user must not work.
		return accountState{}, fmt.Errorf("no such account")
	}

	state := accountState{disabled: out.Users[0].Disabled, fetchedAt: i.clock()}
	if secs, err := strconv.ParseInt(out.Users[0].ValidSince, 10, 64); err == nil && secs > 0 {
		state.validSince = time.Unix(secs, 0)
	}
	return state, nil
}
