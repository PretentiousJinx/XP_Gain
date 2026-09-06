package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testProject = "xpgain-prod"
	testKID     = "test-kid-1"
	testUID     = "firebase-uid-abc123"
)

var fixedNow = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

type minter struct {
	key *rsa.PrivateKey
	kid string
}

func newMinter(t *testing.T) *minter {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return &minter{key: key, kid: testKID}
}

func (m *minter) source() StaticKeys {
	return StaticKeys{m.kid: &m.key.PublicKey}
}

// claims returns a well-formed Firebase claim set that individual tests mutate
// to express exactly one defect each.
func (m *minter) claims() jwt.MapClaims {
	return jwt.MapClaims{
		"aud":       testProject,
		"iss":       issuerPrefix + testProject,
		"sub":       testUID,
		"user_id":   testUID,
		"exp":       fixedNow.Add(time.Hour).Unix(),
		"iat":       fixedNow.Add(-time.Minute).Unix(),
		"auth_time": fixedNow.Add(-time.Minute).Unix(),
		"email":     "player@example.com",
		"firebase":  map[string]any{"sign_in_provider": "password"},
	}
}

func (m *minter) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = m.kid
	s, err := tok.SignedString(m.key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

func newVerifier(t *testing.T, m *minter) *Verifier {
	t.Helper()
	v, err := NewVerifier(testProject,
		WithKeySource(m.source()),
		WithClock(func() time.Time { return fixedNow }))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

// --- the happy path --------------------------------------------------------

func TestValidTokenIsAccepted(t *testing.T) {
	m := newMinter(t)
	v := newVerifier(t, m)

	tok, err := v.Verify(context.Background(), m.sign(t, m.claims()))
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if tok.UID != testUID {
		t.Errorf("UID = %q, want %q", tok.UID, testUID)
	}
	if tok.Email != "player@example.com" {
		t.Errorf("Email = %q", tok.Email)
	}
	if tok.SignInProvider != "password" {
		t.Errorf("SignInProvider = %q, want password", tok.SignInProvider)
	}
}

// --- forgery and algorithm confusion ---------------------------------------

func TestTokenSignedByAnotherKeyIsRejected(t *testing.T) {
	m := newMinter(t)
	attacker := newMinter(t) // different private key, same advertised kid

	v := newVerifier(t, m) // verifier only trusts m's public key

	if _, err := v.Verify(context.Background(), attacker.sign(t, attacker.claims())); err == nil {
		t.Fatal("a token signed by an untrusted key was accepted")
	} else if !errors.Is(err, ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
}

func TestAlgNoneIsRejected(t *testing.T) {
	m := newMinter(t)
	v := newVerifier(t, m)

	tok := jwt.NewWithClaims(jwt.SigningMethodNone, m.claims())
	tok.Header["kid"] = m.kid
	unsigned, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("mint alg=none: %v", err)
	}

	if _, err := v.Verify(context.Background(), unsigned); err == nil {
		t.Fatal("an alg=none token was accepted; signature verification is bypassable")
	}
}

func TestHMACSignedWithPublicKeyIsRejected(t *testing.T) {
	// The classic algorithm-confusion attack: take the RSA public key (which is
	// public by definition), and use its bytes as an HMAC shared secret. A
	// verifier that trusts the token's own alg header would accept this.
	m := newMinter(t)
	v := newVerifier(t, m)

	pubDER, err := x509.MarshalPKIXPublicKey(&m.key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, m.claims())
	tok.Header["kid"] = m.kid
	forged, err := tok.SignedString(pubPEM)
	if err != nil {
		t.Fatalf("mint HS256: %v", err)
	}

	if _, err := v.Verify(context.Background(), forged); err == nil {
		t.Fatal("an HS256 token keyed on the public key was accepted")
	}
}

func TestTamperedPayloadIsRejected(t *testing.T) {
	m := newMinter(t)
	v := newVerifier(t, m)

	raw := m.sign(t, m.claims())
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT parts, got %d", len(parts))
	}
	// Swap one character of the payload; the signature no longer matches.
	payload := []byte(parts[1])
	if payload[0] == 'A' {
		payload[0] = 'B'
	} else {
		payload[0] = 'A'
	}
	tampered := parts[0] + "." + string(payload) + "." + parts[2]

	if _, err := v.Verify(context.Background(), tampered); err == nil {
		t.Fatal("a tampered payload was accepted")
	}
}

// --- project binding -------------------------------------------------------

func TestTokenFromAnotherFirebaseProjectIsRejected(t *testing.T) {
	// This is the check the project ID exists for. The token is genuinely
	// signed by Google and entirely valid -- for someone else's project.
	m := newMinter(t)
	v := newVerifier(t, m)

	c := m.claims()
	c["aud"] = "some-other-app"
	c["iss"] = issuerPrefix + "some-other-app"

	if _, err := v.Verify(context.Background(), m.sign(t, c)); err == nil {
		t.Fatal("a token minted for a different Firebase project was accepted")
	}
}

func TestWrongAudienceIsRejected(t *testing.T) {
	// Isolates the `aud` check by leaving every other claim correct, so this
	// test can only pass because audience binding works.
	m := newMinter(t)
	v := newVerifier(t, m)

	c := m.claims()
	c["aud"] = "some-other-app"

	if _, err := v.Verify(context.Background(), m.sign(t, c)); err == nil {
		t.Fatal("a token addressed to a different audience was accepted")
	}
}

func TestWrongIssuerIsRejected(t *testing.T) {
	m := newMinter(t)
	v := newVerifier(t, m)

	c := m.claims()
	c["iss"] = "https://evil.example.com/" + testProject

	if _, err := v.Verify(context.Background(), m.sign(t, c)); err == nil {
		t.Fatal("a token with a forged issuer was accepted")
	}
}

// --- temporal claims -------------------------------------------------------

func TestExpiredTokenIsRejectedAsExpired(t *testing.T) {
	m := newMinter(t)
	v := newVerifier(t, m)

	c := m.claims()
	c["exp"] = fixedNow.Add(-time.Minute).Unix()

	_, err := v.Verify(context.Background(), m.sign(t, c))
	if err == nil {
		t.Fatal("an expired token was accepted")
	}
	// The distinction matters: the client should refresh, not re-authenticate.
	if !errors.Is(err, ErrExpired) {
		t.Errorf("err = %v, want ErrExpired", err)
	}
}

func TestMissingExpiryIsRejected(t *testing.T) {
	m := newMinter(t)
	v := newVerifier(t, m)

	c := m.claims()
	delete(c, "exp")

	if _, err := v.Verify(context.Background(), m.sign(t, c)); err == nil {
		t.Fatal("a token with no expiry was accepted; it would be valid forever")
	}
}

func TestFutureIssuedAtIsRejected(t *testing.T) {
	m := newMinter(t)
	v := newVerifier(t, m)

	c := m.claims()
	c["iat"] = fixedNow.Add(time.Hour).Unix()

	if _, err := v.Verify(context.Background(), m.sign(t, c)); err == nil {
		t.Fatal("a token issued in the future was accepted")
	}
}

func TestFutureAuthTimeIsRejected(t *testing.T) {
	m := newMinter(t)
	v := newVerifier(t, m)

	c := m.claims()
	c["auth_time"] = fixedNow.Add(time.Hour).Unix()

	if _, err := v.Verify(context.Background(), m.sign(t, c)); err == nil {
		t.Fatal("a token claiming future authentication was accepted")
	}
}

func TestTokenExpiringWithinLeewayIsStillAccepted(t *testing.T) {
	m := newMinter(t)
	v := newVerifier(t, m)

	c := m.claims()
	c["exp"] = fixedNow.Add(-DefaultLeeway / 2).Unix()

	if _, err := v.Verify(context.Background(), m.sign(t, c)); err != nil {
		t.Errorf("token inside the skew window should pass: %v", err)
	}
}

// --- subject ---------------------------------------------------------------

func TestEmptySubjectIsRejected(t *testing.T) {
	m := newMinter(t)
	v := newVerifier(t, m)

	c := m.claims()
	c["sub"] = ""

	if _, err := v.Verify(context.Background(), m.sign(t, c)); err == nil {
		t.Fatal("a token with no subject was accepted; there is no user to act as")
	}
}

func TestOversizedSubjectIsRejected(t *testing.T) {
	m := newMinter(t)
	v := newVerifier(t, m)

	c := m.claims()
	c["sub"] = strings.Repeat("a", maxSubjectLen+1)

	if _, err := v.Verify(context.Background(), m.sign(t, c)); err == nil {
		t.Fatal("an over-long subject was accepted")
	}
}

// --- key resolution --------------------------------------------------------

func TestUnknownKeyIDIsRejected(t *testing.T) {
	m := newMinter(t)
	v := newVerifier(t, m)

	other := &minter{key: m.key, kid: "rotated-away-kid"}
	if _, err := v.Verify(context.Background(), other.sign(t, other.claims())); err == nil {
		t.Fatal("a token naming an unknown kid was accepted")
	}
}

func TestMissingKeyIDIsRejected(t *testing.T) {
	m := newMinter(t)
	v := newVerifier(t, m)

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, m.claims())
	// deliberately no kid header
	raw, err := tok.SignedString(m.key)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := v.Verify(context.Background(), raw); err == nil {
		t.Fatal("a token with no kid was accepted")
	}
}

// --- construction ----------------------------------------------------------

func TestVerifierRequiresProjectID(t *testing.T) {
	if _, err := NewVerifier(""); err == nil {
		t.Fatal("an empty project ID must be refused; it disables audience binding")
	}
}

func TestGarbageInputIsRejected(t *testing.T) {
	m := newMinter(t)
	v := newVerifier(t, m)

	for _, raw := range []string{
		"",
		"not-a-jwt",
		"a.b",
		"a.b.c.d",
		"....",
		strings.Repeat("A", 5000),
	} {
		if _, err := v.Verify(context.Background(), raw); err == nil {
			t.Errorf("garbage input %q was accepted", truncate(raw))
		}
	}
}

func truncate(s string) string {
	if len(s) > 32 {
		return s[:32] + "..."
	}
	return s
}

// --- revocation ------------------------------------------------------------

type fakeRevoker struct {
	err    error
	calls  int
	gotUID string
	gotAT  time.Time
}

func (f *fakeRevoker) CheckRevoked(_ context.Context, uid string, authTime time.Time) error {
	f.calls++
	f.gotUID = uid
	f.gotAT = authTime
	return f.err
}

func TestRevokedTokenIsRejected(t *testing.T) {
	m := newMinter(t)
	rev := &fakeRevoker{err: ErrRevoked}
	v, err := NewVerifier(testProject,
		WithKeySource(m.source()),
		WithClock(func() time.Time { return fixedNow }),
		WithRevocationChecker(rev))
	if err != nil {
		t.Fatal(err)
	}

	_, err = v.Verify(context.Background(), m.sign(t, m.claims()))
	if !errors.Is(err, ErrRevoked) {
		t.Fatalf("err = %v, want ErrRevoked", err)
	}
}

func TestRevocationCheckReceivesUIDAndAuthTime(t *testing.T) {
	m := newMinter(t)
	rev := &fakeRevoker{}
	v, _ := NewVerifier(testProject,
		WithKeySource(m.source()),
		WithClock(func() time.Time { return fixedNow }),
		WithRevocationChecker(rev))

	if _, err := v.Verify(context.Background(), m.sign(t, m.claims())); err != nil {
		t.Fatal(err)
	}
	if rev.calls != 1 {
		t.Fatalf("revocation checker called %d times, want 1", rev.calls)
	}
	if rev.gotUID != testUID {
		t.Errorf("checker saw uid %q, want %q", rev.gotUID, testUID)
	}
	if rev.gotAT.Unix() != fixedNow.Add(-time.Minute).Unix() {
		t.Errorf("checker saw auth_time %v, want the token's own", rev.gotAT)
	}
}

func TestRevocationIsNotConsultedForAnInvalidToken(t *testing.T) {
	// An upstream call per forged token would let anyone drive our Identity
	// Toolkit quota from outside.
	m := newMinter(t)
	attacker := newMinter(t)
	rev := &fakeRevoker{}
	v, _ := NewVerifier(testProject,
		WithKeySource(m.source()),
		WithClock(func() time.Time { return fixedNow }),
		WithRevocationChecker(rev))

	if _, err := v.Verify(context.Background(), attacker.sign(t, attacker.claims())); err == nil {
		t.Fatal("forged token accepted")
	}
	if rev.calls != 0 {
		t.Errorf("revocation checked %d times for a forged token, want 0", rev.calls)
	}
}

func TestWithoutACheckerVerificationStillWorks(t *testing.T) {
	m := newMinter(t)
	v := newVerifier(t, m) // no revocation checker configured

	if _, err := v.Verify(context.Background(), m.sign(t, m.claims())); err != nil {
		t.Errorf("verification should not require a revocation checker: %v", err)
	}
}

// --- multi-factor -----------------------------------------------------------

func mfaVerifier(t *testing.T, m *minter) *Verifier {
	t.Helper()
	v, err := NewVerifier(testProject,
		WithKeySource(m.source()),
		WithClock(func() time.Time { return fixedNow }),
		WithRequiredSecondFactor())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSingleFactorTokenIsRejectedWhenMFARequired(t *testing.T) {
	m := newMinter(t)
	v := mfaVerifier(t, m)

	// A perfectly valid password sign-in, with no second factor.
	_, err := v.Verify(context.Background(), m.sign(t, m.claims()))
	if !errors.Is(err, ErrSecondFactorRequired) {
		t.Fatalf("err = %v, want ErrSecondFactorRequired", err)
	}
}

func TestSMSSecondFactorSatisfiesTheRequirement(t *testing.T) {
	m := newMinter(t)
	v := mfaVerifier(t, m)

	c := m.claims()
	c["firebase"] = map[string]any{
		"sign_in_provider":      "password",
		"sign_in_second_factor": "phone",
	}

	tok, err := v.Verify(context.Background(), m.sign(t, c))
	if err != nil {
		t.Fatalf("an SMS-verified session was rejected: %v", err)
	}
	if tok.SecondFactor != "phone" {
		t.Errorf("SecondFactor = %q, want phone", tok.SecondFactor)
	}
	if !tok.HasSecondFactor() {
		t.Error("HasSecondFactor should be true")
	}
}

func TestBiometricCannotSatisfyTheSecondFactorRequirement(t *testing.T) {
	// The distinction this whole policy rests on. A device biometric prompt is
	// a local unlock: it mints no token and sets no claim, so a client cannot
	// assert it happened and the server must not accept a session as
	// multi-factor on the strength of it.
	m := newMinter(t)
	v := mfaVerifier(t, m)

	// Whatever a client might try to smuggle in, it is not a Firebase claim.
	c := m.claims()
	c["biometric_verified"] = true
	c["device_unlocked"] = true
	c["firebase"] = map[string]any{"sign_in_provider": "password"}

	if _, err := v.Verify(context.Background(), m.sign(t, c)); !errors.Is(err, ErrSecondFactorRequired) {
		t.Fatalf("a client-asserted biometric flag was accepted as a second factor: %v", err)
	}
}

func TestSecondFactorIsReportedWhenNotRequired(t *testing.T) {
	// Even with enforcement off, the claim is surfaced so an audit or a
	// per-action policy can use it.
	m := newMinter(t)
	v := newVerifier(t, m)

	c := m.claims()
	c["firebase"] = map[string]any{
		"sign_in_provider":      "password",
		"sign_in_second_factor": "phone",
	}

	tok, err := v.Verify(context.Background(), m.sign(t, c))
	if err != nil {
		t.Fatal(err)
	}
	if tok.SecondFactor != "phone" {
		t.Errorf("SecondFactor = %q, want it surfaced even when not enforced", tok.SecondFactor)
	}
}

func TestSingleFactorIsAcceptedWhenNotRequired(t *testing.T) {
	m := newMinter(t)
	v := newVerifier(t, m)

	tok, err := v.Verify(context.Background(), m.sign(t, m.claims()))
	if err != nil {
		t.Fatalf("single-factor should pass when MFA is not required: %v", err)
	}
	if tok.HasSecondFactor() {
		t.Error("HasSecondFactor should be false for a password-only session")
	}
}

func TestForgedTokenIsRejectedBeforeTheMFACheck(t *testing.T) {
	// Ordering matters: an attacker must not learn whether MFA is enforced by
	// comparing responses to forged tokens.
	m := newMinter(t)
	attacker := newMinter(t)
	v := mfaVerifier(t, m)

	c := attacker.claims()
	c["firebase"] = map[string]any{
		"sign_in_provider":      "password",
		"sign_in_second_factor": "phone",
	}

	err := func() error {
		_, e := v.Verify(context.Background(), attacker.sign(t, c))
		return e
	}()
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid for a forged token", err)
	}
}
