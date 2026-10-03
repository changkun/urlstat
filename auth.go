// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"cmp"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"

	"latere.ai/x/pkg/authkit"
	"latere.ai/x/pkg/jwtauth"
)

// latereVerifier accepts the RS256 access tokens that auth.latere.ai issues
// to changkun.de pages through browser PKCE, the same login the main site
// and the blog use (see https://changkun.de/login-sdk.js).
//
// Signature, issuer and expiry come from the JWKS document. The right to
// delete statistics does not: any latere account can mint a token, so a
// valid signature only proves who is calling. The allowlist decides whether
// that principal may manage the statistics.
type latereVerifier struct {
	auth    *authkit.JWT
	allowed map[string]bool // lowercased email or principal id (sub)
	log     *log.Logger
}

// newLatereVerifier builds a verifier from the environment. It returns nil
// when AUTH_ALLOWED_PRINCIPALS is unset: with no allowlist there is no safe
// answer, so every token is refused rather than trusted wholesale.
func newLatereVerifier(l *log.Logger) *latereVerifier {
	allowed := principalSet(os.Getenv("AUTH_ALLOWED_PRINCIPALS"))
	if len(allowed) == 0 {
		l.Println("AUTH_ALLOWED_PRINCIPALS is unset, nobody can manage the statistics")
		return nil
	}

	issuer := strings.TrimRight(cmp.Or(os.Getenv("AUTH_URL"), "https://auth.latere.ai"), "/")
	jwks := cmp.Or(os.Getenv("AUTH_JWKS_URL"), issuer+"/.well-known/jwks.json")
	l.Printf("latere auth enabled: issuer=%s principals=%d", issuer, len(allowed))

	return &latereVerifier{
		auth:    authkit.NewJWT(jwtauth.New(jwtauth.Config{JWKSURL: jwks, Issuer: issuer}), nil),
		allowed: allowed,
		log:     l,
	}
}

// principalSet parses a comma-separated list of emails and principal ids.
func principalSet(s string) map[string]bool {
	set := map[string]bool{}
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			set[p] = true
		}
	}
	return set
}

// principal returns who is calling, as an email or else a principal id, when
// r carries a latere token that belongs to an allowlisted principal.
//
// A nil receiver admits nobody. newLatereVerifier returns nil when no
// allowlist is configured, and that must fail closed rather than panic.
func (v *latereVerifier) principal(r *http.Request) (string, bool) {
	if v == nil {
		return "", false
	}
	id, err := v.auth.Authenticate(r)
	if err != nil {
		return "", false
	}
	if v.allowed[strings.ToLower(id.Email)] || v.allowed[strings.ToLower(id.Sub)] {
		return cmp.Or(id.Email, id.Sub), true
	}
	v.log.Printf("latere principal not allowed: sub=%s email=%s client=%s",
		id.Sub, id.Email, id.ClientID)
	return "", false
}

// admin lets only an allowlisted principal through to next.
func admin(latere *latereVerifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, ok := latere.principal(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		latere.log.Printf("%s %s by %s", r.Method, r.URL.Path, who)
		next.ServeHTTP(w, r)
	})
}

// session tells a signed-in page who it is, which is how the dashboard
// learns whether to offer its management tools.
func session(latere *latereVerifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		who, ok := latere.principal(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"principal": who})
	}
}
