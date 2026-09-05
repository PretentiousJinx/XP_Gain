// Package auth verifies Firebase Auth ID tokens.
package auth

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GoogleCertsURL serves the x509 certificates Firebase signs ID tokens with.
// Google rotates these roughly daily, so they cannot be pinned or vendored.
const GoogleCertsURL = "https://www.googleapis.com/robot/v1/metadata/x509/securetoken@system.gserviceaccount.com"

// KeySource resolves a JWT `kid` to the public key that signed it.
type KeySource interface {
	Key(ctx context.Context, kid string) (*rsa.PublicKey, error)
}

// GoogleCerts is a caching KeySource backed by Google's certificate endpoint.
//
// Safe for concurrent use. Reads take a read lock; only a refresh takes the
// write lock, and refreshes are collapsed so a burst of requests arriving on a
// cold or newly-rotated cache produces one upstream fetch rather than one per
// request.
type GoogleCerts struct {
	URL    string
	Client *http.Client
	Now    func() time.Time

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	expiresAt time.Time

	// fetching collapses concurrent refreshes.
	fetchMu sync.Mutex
	// lastAttempt rate-limits refreshes triggered by an unknown kid, so a
	// stream of tokens with bogus kids cannot be turned into a request
	// amplifier against Google.
	lastAttempt time.Time
}

// NewGoogleCerts builds a certificate cache with sensible defaults.
func NewGoogleCerts() *GoogleCerts {
	return &GoogleCerts{
		URL:    GoogleCertsURL,
		Client: &http.Client{Timeout: 10 * time.Second},
		Now:    time.Now,
	}
}

const minRefreshInterval = 30 * time.Second

// Key returns the public key for kid, refreshing the cache if the key is
// unknown or the cache has expired.
func (g *GoogleCerts) Key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if kid == "" {
		return nil, fmt.Errorf("token header has no kid")
	}

	g.mu.RLock()
	key, ok := g.keys[kid]
	fresh := g.now().Before(g.expiresAt)
	g.mu.RUnlock()

	if ok && fresh {
		return key, nil
	}

	if err := g.refresh(ctx, ok); err != nil {
		// A stale-but-usable key beats failing the request when Google is
		// briefly unreachable: the signature is still valid, and the token's
		// own exp still bounds how long it is accepted.
		if ok {
			return key, nil
		}
		return nil, err
	}

	g.mu.RLock()
	defer g.mu.RUnlock()
	if key, ok := g.keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("no Google signing key matches kid %q", kid)
}

func (g *GoogleCerts) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// refresh fetches the certificate set. haveStaleKey relaxes nothing about
// verification; it only decides whether the unknown-kid rate limit applies.
func (g *GoogleCerts) refresh(ctx context.Context, haveStaleKey bool) error {
	g.fetchMu.Lock()
	defer g.fetchMu.Unlock()

	// Another goroutine may have refreshed while we waited for the lock.
	g.mu.RLock()
	fresh := g.now().Before(g.expiresAt)
	g.mu.RUnlock()
	if fresh && haveStaleKey {
		return nil
	}

	if since := g.now().Sub(g.lastAttempt); since < minRefreshInterval && !g.lastAttempt.IsZero() {
		if fresh {
			return nil
		}
		return fmt.Errorf("key refresh rate-limited; retry in %s", (minRefreshInterval - since).Round(time.Second))
	}
	g.lastAttempt = g.now()

	url := g.URL
	if url == "" {
		url = GoogleCertsURL
	}
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build cert request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch Google certs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch Google certs: unexpected status %s", resp.Status)
	}

	var pems map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&pems); err != nil {
		return fmt.Errorf("decode Google certs: %w", err)
	}
	if len(pems) == 0 {
		return fmt.Errorf("Google certs response was empty")
	}

	keys := make(map[string]*rsa.PublicKey, len(pems))
	for kid, certPEM := range pems {
		key, err := parseRSACert(certPEM)
		if err != nil {
			// One malformed entry must not discard the whole rotation set.
			continue
		}
		keys[kid] = key
	}
	if len(keys) == 0 {
		return fmt.Errorf("Google certs response contained no usable RSA keys")
	}

	g.mu.Lock()
	g.keys = keys
	g.expiresAt = g.now().Add(cacheTTL(resp.Header.Get("Cache-Control")))
	g.mu.Unlock()
	return nil
}

func parseRSACert(certPEM string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil, fmt.Errorf("not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("certificate public key is %T, want RSA", cert.PublicKey)
	}
	return key, nil
}

// defaultCacheTTL is used when Cache-Control is absent or unparseable. Google
// normally advertises several hours; an hour is a safe conservative floor.
const defaultCacheTTL = time.Hour

func cacheTTL(cacheControl string) time.Duration {
	for _, part := range strings.Split(cacheControl, ",") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "max-age=") {
			continue
		}
		secs, err := strconv.Atoi(strings.TrimPrefix(part, "max-age="))
		if err != nil || secs <= 0 {
			break
		}
		return time.Duration(secs) * time.Second
	}
	return defaultCacheTTL
}

// StaticKeys is a fixed KeySource, used by tests.
type StaticKeys map[string]*rsa.PublicKey

func (s StaticKeys) Key(_ context.Context, kid string) (*rsa.PublicKey, error) {
	if key, ok := s[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("no key for kid %q", kid)
}
