package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/PretentiousJinx/xpgain/server/internal/auth"
)

// Authenticator verifies a raw bearer token and returns the identity it asserts.
type Authenticator interface {
	Verify(ctx context.Context, rawToken string) (*auth.Token, error)
}

type ctxKey struct{}

// userIDKey is unexported and of a private type, so nothing outside this
// package can inject a user ID into a request context and impersonate a user.
var userIDKey ctxKey

// UserIDFrom returns the authenticated user ID placed by requireAuth.
func UserIDFrom(ctx context.Context) (string, bool) {
	uid, ok := ctx.Value(userIDKey).(string)
	return uid, ok && uid != ""
}

// bearerToken pulls the credential out of the Authorization header.
//
// The scheme match is case-insensitive because RFC 7235 defines it that way and
// real clients do send "bearer".
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	scheme, rest, found := strings.Cut(h, " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return "", false
	}
	token := strings.TrimSpace(rest)
	return token, token != ""
}

// requireAuth rejects any request without a valid Firebase ID token.
//
// The handler behind this middleware can rely on UserIDFrom returning a
// verified UID; it never sees an unauthenticated request. Identity is taken
// only from the verified token -- never from a header, query parameter, or
// request body -- so a client cannot name the account it is acting on.
func (a *API) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := bearerToken(r)
		if !ok {
			unauthorized(w, "unauthorized", "Sign in required.")
			return
		}

		tok, err := a.auth.Verify(r.Context(), raw)
		if err != nil {
			// The reason is logged but never returned: telling a caller
			// exactly why a token failed helps forge the next one.
			slog.Info("rejected token", "path", r.URL.Path, "err", err)
			if errors.Is(err, auth.ErrExpired) {
				unauthorized(w, "token_expired", "Your session expired. Please retry.")
				return
			}
			unauthorized(w, "unauthorized", "Sign in required.")
			return
		}

		ctx := context.WithValue(r.Context(), userIDKey, tok.UID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func unauthorized(w http.ResponseWriter, code, msg string) {
	// WWW-Authenticate tells a conforming client this is an auth failure it can
	// recover from by refreshing, rather than a permanent rejection.
	w.Header().Set("WWW-Authenticate", `Bearer realm="xpgain"`)
	writeJSON(w, http.StatusUnauthorized, ErrorBody{Code: code, Message: msg})
}
