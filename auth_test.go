// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"latere.ai/x/pkg/authkit"
	"latere.ai/x/pkg/jwtauth"
)

const testKid = "test-key"

// authFixture is a latere auth service stood up in-process: an RSA key, the
// JWKS document served over HTTP, and a minter for tokens signed by that key.
type authFixture struct {
	key    *rsa.PrivateKey
	issuer string
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("cannot generate key: %v", err)
	}

	f := &authFixture{key: key}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA",
			"kid": testKid,
			"alg": "RS256",
			"use": "sig",
			"n":   b64(key.N.Bytes()),
			"e":   b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(srv.Close)
	f.issuer = srv.URL
	return f
}

// token mints an access token shaped like the one auth.latere.ai issues to
// the changkun-blog PKCE client.
func (f *authFixture) token(t *testing.T, claims map[string]any) string {
	t.Helper()

	payload := map[string]any{
		"sub":            "principal-1",
		"iss":            f.issuer,
		"exp":            time.Now().Add(time.Hour).Unix(),
		"principal_type": "user",
		"client_id":      "changkun-blog",
	}
	maps.Copy(payload, claims)

	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": testKid, "typ": "JWT"})
	if err != nil {
		t.Fatalf("cannot encode header: %v", err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("cannot encode payload: %v", err)
	}

	signing := b64(header) + "." + b64(body)
	sum := crypto.SHA256.New()
	sum.Write([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum.Sum(nil))
	if err != nil {
		t.Fatalf("cannot sign token: %v", err)
	}
	return signing + "." + b64(sig)
}

func (f *authFixture) verifier(allowed string) *latereVerifier {
	return &latereVerifier{
		auth: authkit.NewJWT(jwtauth.New(jwtauth.Config{
			JWKSURL: f.issuer + "/.well-known/jwks.json",
			Issuer:  f.issuer,
		}), nil),
		allowed: principalSet(allowed),
		log:     log.New(io.Discard, "", 0),
	}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// doAdmin calls a handler mounted behind admin and reports the status and
// whether the handler was reached.
func doAdmin(v *latereVerifier, bearer string) (int, bool) {
	reached := false
	h := admin(v, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodPost, "/urlstat/dashboard/cleanup", nil)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, reached
}

// TestAdmin covers who may manage the statistics: an allowlisted principal
// holding a token that auth.latere.ai signed, and nobody else.
func TestAdmin(t *testing.T) {
	f := newAuthFixture(t)

	forger := newAuthFixture(t)
	forger.issuer = f.issuer // claims the real issuer, signs with another key

	tests := []struct {
		name    string
		allowed string
		token   string
		want    int
	}{
		{"allowlisted email", "hi@changkun.de", f.token(t, map[string]any{"email": "hi@changkun.de"}), http.StatusOK},
		{"email is case-insensitive", "hi@changkun.de", f.token(t, map[string]any{"email": "Hi@Changkun.de"}), http.StatusOK},
		{"allowlisted principal id", "principal-1", f.token(t, map[string]any{"email": "someone@example.com"}), http.StatusOK},
		{"no token", "hi@changkun.de", "", http.StatusUnauthorized},
		{"not a token", "hi@changkun.de", "nonsense", http.StatusUnauthorized},
		{"valid signature but foreign principal", "hi@changkun.de", f.token(t, map[string]any{"email": "stranger@example.com", "sub": "principal-2"}), http.StatusUnauthorized},
		{"expired token", "hi@changkun.de", f.token(t, map[string]any{"email": "hi@changkun.de", "exp": time.Now().Add(-time.Minute).Unix()}), http.StatusUnauthorized},
		{"foreign issuer", "hi@changkun.de", f.token(t, map[string]any{"email": "hi@changkun.de", "iss": "https://evil.example.com"}), http.StatusUnauthorized},
		{"forged signature", "hi@changkun.de", forger.token(t, map[string]any{"email": "hi@changkun.de"}), http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reached := doAdmin(f.verifier(tt.allowed), tt.token)
			if got != tt.want {
				t.Fatalf("status = %d, want %d", got, tt.want)
			}
			if reached != (tt.want == http.StatusOK) {
				t.Fatalf("handler reached = %v with status %d", reached, got)
			}
		})
	}
}

// TestAdminWithoutAllowlist pins the closed default: with no allowlist the
// verifier is nil, and a nil verifier admits nobody, a valid token included.
func TestAdminWithoutAllowlist(t *testing.T) {
	f := newAuthFixture(t)
	t.Setenv("AUTH_ALLOWED_PRINCIPALS", "")
	v := newLatereVerifier(log.New(io.Discard, "", 0))
	if v != nil {
		t.Fatalf("verifier = %+v without an allowlist, want nil", v)
	}
	if got, reached := doAdmin(v, f.token(t, map[string]any{"email": "hi@changkun.de"})); got != http.StatusUnauthorized || reached {
		t.Fatalf("status = %d, reached = %v, want %d and not reached", got, reached, http.StatusUnauthorized)
	}
}

// TestSession checks that a signed-in page learns who it is, and that
// anyone else learns nothing.
func TestSession(t *testing.T) {
	f := newAuthFixture(t)
	h := session(f.verifier("hi@changkun.de"))

	r := httptest.NewRequest(http.MethodGet, "/urlstat/dashboard/session", nil)
	r.Header.Set("Authorization", "Bearer "+f.token(t, map[string]any{"email": "hi@changkun.de"}))
	w := httptest.NewRecorder()
	h(w, r)
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); w.Code != http.StatusOK || err != nil || got["principal"] != "hi@changkun.de" {
		t.Fatalf("session = %d %s, want 200 and the principal", w.Code, w.Body)
	}

	w = httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/urlstat/dashboard/session", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("session without a token = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}
