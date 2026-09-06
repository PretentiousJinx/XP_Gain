package auth

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Sentinel errors. ErrExpired is separated from ErrInvalid because the client
// reaction differs: an expired token means "refresh and retry", while anything
// else means "sign in again".
var (
	ErrExpired = errors.New("auth: id token expired")
	ErrInvalid = errors.New("auth: id token invalid")
	ErrRevoked = errors.New("auth: id token revoked")

	// ErrSecondFactorRequired means the credential is genuine but the account
	// has not completed multi-factor sign-in.
	ErrSecondFactorRequired = errors.New("auth: second factor required")
)

// RevocationChecker reports whether a still-valid signature belongs to a
// session that has since been revoked.
//
// Signature verification cannot answer this on its own: an ID token stays
// cryptographically valid until it expires, so without this check a session
// revoked or a password changed five minutes ago is still honoured for the
// remainder of the token's hour.
type RevocationChecker interface {
	CheckRevoked(ctx context.Context, uid string, authTime time.Time) error
}

// issuerPrefix is the fixed Firebase issuer namespace.
const issuerPrefix = "https://securetoken.google.com/"

// maxSubjectLen is the documented Firebase UID ceiling.
const maxSubjectLen = 128

// Token is the verified identity extracted from an ID token.
type Token struct {
	UID            string
	Email          string
	EmailVerified  bool
	SignInProvider string

	// SecondFactor names the factor used at sign-in ("phone" for SMS), or is
	// empty when the session is single-factor.
	//
	// This is the only multi-factor signal that exists server-side. A device
	// biometric prompt produces no claim and no token of its own, so it cannot
	// be verified here and must never be treated as equivalent.
	SecondFactor string

	AuthTime  time.Time
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// HasSecondFactor reports whether the session completed multi-factor sign-in.
func (t Token) HasSecondFactor() bool { return t.SecondFactor != "" }

type firebaseClaims struct {
	jwt.RegisteredClaims
	AuthTime      int64  `json:"auth_time"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Firebase      struct {
		SignInProvider     string `json:"sign_in_provider"`
		SignInSecondFactor string `json:"sign_in_second_factor"`
	} `json:"firebase"`
}

// Verifier validates Firebase Auth ID tokens for one project.
//
// Safe for concurrent use by many goroutines; the only mutable state lives
// behind the KeySource, which is itself concurrency-safe.
type Verifier struct {
	projectID string
	keys      KeySource
	parser    *jwt.Parser
	now       func() time.Time
	revoked   RevocationChecker

	// requireSecondFactor rejects single-factor sessions outright.
	requireSecondFactor bool
}

// Option configures a Verifier.
type Option func(*Verifier)

// WithKeySource swaps the signing-key provider. Tests use this; production
// should leave the default Google certificate cache in place.
func WithKeySource(ks KeySource) Option {
	return func(v *Verifier) { v.keys = ks }
}

// WithClock injects a deterministic clock for tests.
func WithClock(fn func() time.Time) Option {
	return func(v *Verifier) { v.now = fn }
}

// WithRevocationChecker enables the revocation check. Without it, verification
// is purely cryptographic and a revoked session stays usable until its token
// expires.
func WithRevocationChecker(rc RevocationChecker) Option {
	return func(v *Verifier) { v.revoked = rc }
}

// WithRequiredSecondFactor rejects any token whose session did not complete
// multi-factor sign-in.
//
// Enforced here rather than per-handler so a new endpoint cannot accidentally
// opt out of it: every route behind the verifier inherits the policy.
func WithRequiredSecondFactor() Option {
	return func(v *Verifier) { v.requireSecondFactor = true }
}

// DefaultLeeway absorbs clock skew between this server and Google. It is kept
// deliberately small: leeway on `exp` extends the window in which a revoked or
// expired token is still accepted, so it buys skew tolerance at a real cost.
const DefaultLeeway = 10 * time.Second

// NewVerifier builds a verifier for the given Firebase project ID.
//
// The project ID is mandatory and is the security boundary: it is checked
// against the token's `aud` and `iss`. Without it, a valid ID token minted for
// *any other Firebase project* would authenticate here.
func NewVerifier(projectID string, opts ...Option) (*Verifier, error) {
	if projectID == "" {
		return nil, errors.New("auth: project ID is required")
	}

	v := &Verifier{
		projectID: projectID,
		keys:      NewGoogleCerts(),
		now:       time.Now,
	}
	for _, opt := range opts {
		opt(v)
	}

	v.parser = jwt.NewParser(
		// Pin the algorithm. Without this, a token claiming `alg: none` -- or
		// an HMAC token forged using the public key as the shared secret --
		// would be accepted. This single option closes both classic JWT
		// algorithm-confusion attacks.
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithAudience(projectID),
		jwt.WithIssuer(issuerPrefix+projectID),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(DefaultLeeway),
		jwt.WithTimeFunc(func() time.Time { return v.now() }),
	)
	return v, nil
}

// Verify checks an ID token and returns the identity it asserts.
//
// Every check Firebase documents is applied: RS256 only, a `kid` matching a
// current Google signing key, valid signature, `aud` equal to the project ID,
// `iss` equal to the project issuer, `exp` in the future, `iat` and `auth_time`
// in the past, and a non-empty `sub` within the UID length limit.
func (v *Verifier) Verify(ctx context.Context, raw string) (*Token, error) {
	if raw == "" {
		return nil, fmt.Errorf("%w: empty token", ErrInvalid)
	}

	claims := &firebaseClaims{}
	_, err := v.parser.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		key, err := v.keys.Key(ctx, kid)
		if err != nil {
			return nil, err
		}
		// Belt and braces: the parser already pinned RS256, so the key must be
		// RSA. Assert it rather than trusting the type flowing through `any`.
		var _ *rsa.PublicKey = key
		return key, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, fmt.Errorf("%w: %v", ErrExpired, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	// `sub` carries the Firebase UID and is what the rest of the system treats
	// as the account identity, so it is validated explicitly rather than being
	// assumed non-empty.
	uid := claims.Subject
	if uid == "" {
		return nil, fmt.Errorf("%w: token has no subject", ErrInvalid)
	}
	if len(uid) > maxSubjectLen {
		return nil, fmt.Errorf("%w: subject exceeds %d characters", ErrInvalid, maxSubjectLen)
	}

	now := v.now()
	if claims.AuthTime > 0 {
		authTime := time.Unix(claims.AuthTime, 0)
		if authTime.After(now.Add(DefaultLeeway)) {
			return nil, fmt.Errorf("%w: auth_time is in the future", ErrInvalid)
		}
	}

	// Revocation is checked last, once the token is known to be authentic and
	// current. Doing it earlier would spend an upstream call on tokens that a
	// local check was going to reject anyway.
	if v.revoked != nil {
		var authTime time.Time
		if claims.AuthTime > 0 {
			authTime = time.Unix(claims.AuthTime, 0)
		}
		if err := v.revoked.CheckRevoked(ctx, uid, authTime); err != nil {
			return nil, err
		}
	}

	// The second factor is checked after authenticity but before revocation, so
	// a single-factor token never costs an upstream lookup.
	if v.requireSecondFactor && claims.Firebase.SignInSecondFactor == "" {
		return nil, fmt.Errorf("%w: sign-in used only %q",
			ErrSecondFactorRequired, claims.Firebase.SignInProvider)
	}

	tok := &Token{
		UID:            uid,
		Email:          claims.Email,
		EmailVerified:  claims.EmailVerified,
		SignInProvider: claims.Firebase.SignInProvider,
		SecondFactor:   claims.Firebase.SignInSecondFactor,
	}
	if claims.AuthTime > 0 {
		tok.AuthTime = time.Unix(claims.AuthTime, 0)
	}
	if claims.IssuedAt != nil {
		tok.IssuedAt = claims.IssuedAt.Time
	}
	if claims.ExpiresAt != nil {
		tok.ExpiresAt = claims.ExpiresAt.Time
	}
	return tok, nil
}
