package authkit

import (
	"net/http"
	"strings"
)

// StaticTokenAuthenticator maps a small set of pre-shared bearer tokens to
// caller-supplied Identities. Each token resolves to its own Identity, so
// distinct dev users, fixtures, or service callers can be modelled without
// the single-superadmin synthesis BearerToken applies.
//
// Constant-time comparison is used against every configured token so timing
// does not leak which token (if any) matched. Construction with an empty or
// nil map produces an authenticator that always returns ErrUnauthenticated.
type StaticTokenAuthenticator struct {
	tokens map[string]Identity
}

// NewStaticToken wires a static-token authenticator from a token → Identity
// map. Each Identity in tokens is automatically stamped with AuthMethod =
// MethodStatic; any explicit AuthMethod set by the caller is overwritten.
func NewStaticToken(tokens map[string]Identity) *StaticTokenAuthenticator {
	stamped := make(map[string]Identity, len(tokens))
	for tok, id := range tokens {
		id.AuthMethod = MethodStatic
		stamped[tok] = id
	}
	return &StaticTokenAuthenticator{tokens: stamped}
}

func (s *StaticTokenAuthenticator) Authenticate(r *http.Request) (Identity, error) {
	if s == nil || len(s.tokens) == 0 {
		return Identity{}, ErrUnauthenticated
	}
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if !strings.HasPrefix(h, p) {
		return Identity{}, ErrUnauthenticated
	}
	candidate := h[len(p):]
	// Compare against every configured token in constant time so timing
	// does not reveal which prefix matched (or that none did).
	var match Identity
	matched := false
	for tok, id := range s.tokens {
		if constantEq(candidate, tok) {
			match = id
			matched = true
			// keep going so the loop's work is independent of the
			// match position
		}
	}
	if !matched {
		return Identity{}, ErrUnauthenticated
	}
	return match, nil
}
