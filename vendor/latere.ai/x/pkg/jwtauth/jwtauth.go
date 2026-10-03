// Package jwtauth provides JWKS-based RS256 JWT validation for services
// that accept tokens issued by the Latere auth service.
//
// # Design
//
// The auth service issues RS256 JWTs with claims that vary by principal type.
// Downstream services validate these tokens locally using the
// public keys published at the auth service's JWKS endpoint, without any
// round-trip to the auth service.
//
// # Principal types and validation strategies
//
// Every token carries a "principal_type" claim that identifies the subject:
//
//   - "user"    — a human user authenticated via OIDC. Local JWT validation
//     is always sufficient.
//   - "service" — a service account using client_credentials. Local JWT
//     validation is always sufficient.
//   - "agent"   — an AI agent acting on behalf of a delegator (RFC 8693
//     token exchange). Agent tokens carry a "validation" claim:
//   - "local"  — read-only agent; local JWT validation is sufficient.
//   - "strict" — agent with write/delete/admin scopes; the downstream
//     service MUST call GET /tokeninfo on EVERY request to verify
//     that the delegation has not been revoked or expired.
//
// Use [Claims.NeedsTokenInfo] to determine whether a token requires online
// validation. The /tokeninfo call itself is the caller's responsibility.
//
// # JWKS caching
//
// Public keys are fetched from the JWKS endpoint and cached for the duration
// specified by [Config.CacheTTL] (default 5 minutes). On fetch errors the
// validator falls back to stale cached keys, so transient auth-service
// outages do not break validation for already-seen keys.
//
// # Token claims
//
// The [Claims] struct is a superset of all principal types. Fields that do
// not apply to a given principal type are zero-valued:
//
//	Field          User   Service  Agent
//	─────          ────   ───────  ─────
//	Sub            ✓      ✓        ✓
//	PrincipalType  ✓      ✓        ✓
//	OrgID          ✓      ✓        ✓
//	Scopes         ✓      ✓        ✓       (JWT claim key: "scp")
//	Roles          ✓      ✓        ✓
//	Email          ✓
//	ClientID       ✓      ✓        ✓       (JWT claim key: "client_id", "azp" fallback)
//	IsSuperadmin   ✓      ✓        ✓
//	Validation                      ✓       ("local" or "strict")
//	DelegationID                    ✓
//	Act                             ✓       (delegator identity)
//
// # Usage
//
//	v := jwtauth.New(jwtauth.Config{
//	    JWKSURL:   "https://auth.latere.ai/.well-known/jwks.json",
//	    Issuer:    "https://auth.latere.ai",        // optional
//	    Audiences: []string{"my-service-client-id"}, // optional
//	})
//
//	// As HTTP middleware:
//	mux.Handle("GET /api/resource", v.Middleware(handler))
//
//	// In a handler:
//	claims := jwtauth.ClaimsFromContext(r.Context())
//	_ = claims.Sub
package jwtauth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// ── Principal & Validation Types ────────────────────────────────────────────

// PrincipalType is the type of the token subject.
type PrincipalType string

const (
	PrincipalUser    PrincipalType = "user"
	PrincipalService PrincipalType = "service"
)

// ── Claims ──────────────────────────────────────────────────────────────────

// Claims holds verified JWT claims across all principal types.
type Claims struct {
	Sub           string
	Iss           string
	Aud           []string
	Exp           time.Time
	PrincipalType PrincipalType
	OrgID         string
	Email         string // populated for users
	ClientID      string // originating OAuth client ("client_id", "azp" fallback)
	IsSuperadmin  bool
	Scopes        []string
	Roles         []string

	// Kind and ActorID describe a non-principal actor the token is bound
	// to (e.g. Kind=="sandbox", ActorID=<sandbox id>). Generic and
	// domain-agnostic — consumers interpret ActorID according to Kind.
	// Empty for ordinary user/service/agent tokens. Distinct from Act,
	// which is the RFC 8693 delegation actor (a principal sub).
	Kind    string
	ActorID string
}

// ── Errors ──────────────────────────────────────────────────────────────────

var (
	ErrNoToken          = errors.New("jwtauth: missing bearer token")
	ErrMalformedToken   = errors.New("jwtauth: malformed token")
	ErrInvalidSignature = errors.New("jwtauth: invalid signature")
	ErrTokenExpired     = errors.New("jwtauth: token expired")
	ErrTokenNotValidYet = errors.New("jwtauth: token not valid yet")
	ErrInvalidIssuer    = errors.New("jwtauth: invalid issuer")
	ErrInvalidAudience  = errors.New("jwtauth: invalid audience")
	ErrUnsupportedAlg   = errors.New("jwtauth: unsupported algorithm")
)

// ── Config & Validator ──────────────────────────────────────────────────────

// Config holds the settings for JWT validation.
type Config struct {
	// JWKSURL is the JWKS endpoint, e.g. "https://auth.latere.ai/.well-known/jwks.json".
	JWKSURL string
	// Issuer is the expected "iss" claim. Skipped if empty.
	Issuer string
	// Audiences is the set of acceptable "aud" values. Skipped if empty.
	Audiences []string
	// CacheTTL controls how long JWKS keys are cached. Defaults to 5 minutes.
	CacheTTL time.Duration
	// HTTPClient fetches JWKS documents. Defaults to a client with a 10-second
	// timeout. Supply it for custom trust roots, proxies, or mTLS.
	HTTPClient *http.Client
}

// Validator validates RS256 JWTs using keys fetched from a JWKS endpoint.
type Validator struct {
	cfg   Config
	cache *jwksCache
}

// New creates a Validator.
func New(cfg Config) *Validator {
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = 5 * time.Minute
	}
	cache := &jwksCache{url: cfg.JWKSURL, ttl: cfg.CacheTTL}
	if cfg.HTTPClient != nil {
		cache.get = cfg.HTTPClient.Get
	}
	return &Validator{
		cfg:   cfg,
		cache: cache,
	}
}

// ── Package-level vars for testability ──────────────────────────────────────

var httpGet = func(url string) (*http.Response, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	return client.Get(url) //nolint:gosec
}

var timeNow = time.Now

// ── Validate ────────────────────────────────────────────────────────────────

// Validate parses and validates a raw JWT string.
func (v *Validator) Validate(rawToken string) (*Claims, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return nil, ErrMalformedToken
	}

	// Decode header.
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrMalformedToken
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, ErrMalformedToken
	}
	if header.Alg != "RS256" {
		return nil, ErrUnsupportedAlg
	}

	// Verify signature.
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, ErrMalformedToken
	}

	keys, err := v.cache.getKeysForKid(header.Kid)
	if err != nil {
		return nil, fmt.Errorf("jwtauth: fetch JWKS: %w", err)
	}

	sigInput := parts[0] + "." + parts[1]
	digest := hashSHA256([]byte(sigInput))

	if !verifySignature(keys, header.Kid, digest, sig) {
		return nil, ErrInvalidSignature
	}

	// Decode payload.
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrMalformedToken
	}
	var raw rawPayload
	if err := json.Unmarshal(payloadBytes, &raw); err != nil {
		return nil, ErrMalformedToken
	}

	// Validate exp.
	exp := time.Unix(int64(raw.Exp), 0)
	if timeNow().After(exp) {
		return nil, ErrTokenExpired
	}

	// Validate nbf (RFC 7519 §4.1.5): reject a token used before its
	// not-before instant. Tokens that omit nbf are unaffected.
	if raw.Nbf != 0 && timeNow().Before(time.Unix(int64(raw.Nbf), 0)) {
		return nil, ErrTokenNotValidYet
	}

	// Validate iss.
	if v.cfg.Issuer != "" && raw.Iss != v.cfg.Issuer {
		return nil, ErrInvalidIssuer
	}

	// Validate aud.
	if len(v.cfg.Audiences) > 0 {
		if !audMatch(raw.Aud, v.cfg.Audiences) {
			return nil, ErrInvalidAudience
		}
	}

	if raw.Sub == "" {
		return nil, ErrMalformedToken
	}

	return claimsFromRawPayload(raw), nil
}

// claimsFromRawPayload maps a decoded JWT payload onto Claims. It is
// the single mapping site shared by Validate (after full verification)
// and ParseUnverified (transport-trusted, no verification) so the two
// paths can never disagree on which JSON claim feeds which field.
func claimsFromRawPayload(raw rawPayload) *Claims {
	clientID := raw.ClientID
	if clientID == "" {
		clientID = raw.AuthorizedParty
	}
	claims := &Claims{
		Sub:           raw.Sub,
		Iss:           raw.Iss,
		Aud:           []string(raw.Aud),
		Exp:           time.Unix(int64(raw.Exp), 0),
		PrincipalType: PrincipalType(raw.PrincipalType),
		OrgID:         raw.OrgID,
		Email:         raw.Email,
		ClientID:      clientID,
		IsSuperadmin:  raw.IsSuperadmin,
		Scopes:        raw.Scopes,
		Roles:         raw.Roles,
		Kind:          raw.Kind,
		ActorID:       raw.ActorID,
	}
	return claims
}

// ParseUnverified decodes a JWT's payload into Claims WITHOUT
// signature, issuer, audience, or expiration validation. It is
// intended only for tokens already trusted by transport — e.g. an
// access token loaded from an encrypted session cookie minted via the
// OIDC PKCE flow, where the bytes never left a trusted boundary
// unverified. For every other input (Authorization headers, query
// params, log fields, anything off the wire), use Validate.
//
// It performs no network I/O (no JWKS fetch) and the only structural
// checks are: three dot-separated segments, a base64url-decodable
// JSON payload, and a non-empty sub. Exp is populated on the returned
// Claims so callers can apply their own lifecycle policy, but it is
// not enforced here.
func ParseUnverified(rawToken string) (*Claims, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return nil, ErrMalformedToken
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrMalformedToken
	}
	var raw rawPayload
	if err := json.Unmarshal(payloadBytes, &raw); err != nil {
		return nil, ErrMalformedToken
	}
	if raw.Sub == "" {
		return nil, ErrMalformedToken
	}
	return claimsFromRawPayload(raw), nil
}

// ── Middleware ───────────────────────────────────────────────────────────────

type ctxKey int

const ctxKeyClaims ctxKey = iota

// Middleware returns HTTP middleware that validates the JWT from the
// Authorization: Bearer header and injects Claims into the request context.
func (v *Validator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			WriteUnauthorized(w, ErrNoToken.Error())
			return
		}
		token := auth[7:]

		claims, err := v.Validate(token)
		if err != nil {
			WriteUnauthorized(w, err.Error())
			return
		}

		ctx := context.WithValue(r.Context(), ctxKeyClaims, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ClaimsFromContext extracts validated Claims from the context.
func ClaimsFromContext(ctx context.Context) *Claims {
	c, _ := ctx.Value(ctxKeyClaims).(*Claims)
	return c
}

// ── JWKS Cache ──────────────────────────────────────────────────────────────

type jwkEntry struct {
	kid string
	pub *rsa.PublicKey
}

// minForcedRefreshInterval bounds how often a kid miss may force a JWKS
// refetch, so a flood of tokens carrying unknown kids cannot hammer the
// endpoint.
const minForcedRefreshInterval = 15 * time.Second

type jwksCache struct {
	url        string
	ttl        time.Duration
	get        func(string) (*http.Response, error)
	mu         sync.Mutex // guards cachedAt, lastForced, keys; never held across I/O
	fetchMu    sync.Mutex // serializes JWKS fetches so only one goroutine hits the network
	cachedAt   time.Time
	lastForced time.Time
	keys       []jwkEntry
}

// getKeys returns the cached JWKS, refetching only when the TTL has elapsed.
func (c *jwksCache) getKeys() ([]jwkEntry, error) {
	return c.load(false)
}

// getKeysForKid returns the cached JWKS but, when no cached key matches kid,
// forces a single TTL-bypassing refetch (rate-limited by
// minForcedRefreshInterval) so a freshly rotated signing key is picked up
// without waiting out the whole CacheTTL.
func (c *jwksCache) getKeysForKid(kid string) ([]jwkEntry, error) {
	keys, err := c.load(false)
	if err != nil {
		return keys, err
	}
	if kid == "" || slices.ContainsFunc(keys, func(k jwkEntry) bool { return k.kid == kid }) {
		return keys, nil
	}

	// Claim the forced-refresh window atomically: a flood of unknown-kid tokens
	// triggers at most one refetch per minForcedRefreshInterval.
	c.mu.Lock()
	if timeNow().Sub(c.lastForced) < minForcedRefreshInterval {
		c.mu.Unlock()
		return keys, nil
	}
	c.lastForced = timeNow()
	c.mu.Unlock()

	return c.load(true)
}

// load returns the cached JWKS, fetching from the network only when the cache
// is stale (or force is set). The blocking HTTP fetch runs WITHOUT c.mu held —
// fetchMu serializes fetchers instead — so a concurrent validation whose kid is
// already cached is served from the fast path rather than blocking behind the
// round-trip. The stale-on-error fallback is preserved: any fetch/parse failure
// (or an empty key set) returns the previously cached keys when present.
func (c *jwksCache) load(force bool) ([]jwkEntry, error) {
	if keys, ok := c.freshKeys(force); ok {
		return keys, nil
	}

	// Only one goroutine fetches at a time; the rest wait here, not on c.mu.
	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()

	// A fetch we queued behind may have refreshed the cache while we waited.
	if keys, ok := c.freshKeys(force); ok {
		return keys, nil
	}

	get := c.get
	if get == nil {
		get = httpGet
	}
	resp, err := get(c.url)
	if err != nil {
		return c.cachedOr(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return c.cachedOr(fmt.Errorf("jwtauth: JWKS status %d", resp.StatusCode))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return c.cachedOr(err)
	}

	var jwks struct {
		Keys []jwkRaw `json:"keys"`
	}
	if err := json.Unmarshal(body, &jwks); err != nil {
		return c.cachedOr(err)
	}

	var keys []jwkEntry
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		pub, err := parseRSAPublicKey(k.N, k.E)
		if err != nil {
			continue
		}
		keys = append(keys, jwkEntry{kid: k.Kid, pub: pub})
	}

	if len(keys) == 0 {
		// A well-formed 200 that yields no usable RSA keys must not discard a
		// still-valid cache: keep serving the cache and leave cachedAt untouched
		// so the next request retries (mirrors the error-path fallback).
		if cached, _ := c.cachedOr(nil); len(cached) > 0 {
			return cached, nil
		}
		return keys, nil
	}

	c.mu.Lock()
	c.keys = keys
	c.cachedAt = timeNow()
	c.mu.Unlock()
	return keys, nil
}

// freshKeys returns the cached keys when they are still within TTL and force is
// not set; ok is false when a fetch is required. Held briefly under c.mu.
func (c *jwksCache) freshKeys(force bool) ([]jwkEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && timeNow().Sub(c.cachedAt) < c.ttl && len(c.keys) > 0 {
		return c.keys, true
	}
	return nil, false
}

// cachedOr returns the cached keys when present (the stale-on-error fallback),
// otherwise the supplied error. Held briefly under c.mu.
func (c *jwksCache) cachedOr(err error) ([]jwkEntry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.keys) > 0 {
		return c.keys, nil
	}
	return nil, err
}

type jwkRaw struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func parseRSAPublicKey(nB64, eB64 string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return nil, err
	}
	n := new(big.Int).SetBytes(nBytes)
	e := 0
	for _, b := range eBytes {
		e = e<<8 + int(b)
	}
	return &rsa.PublicKey{N: n, E: e}, nil
}

func hashSHA256(data []byte) []byte {
	h := crypto.SHA256.New()
	h.Write(data)
	return h.Sum(nil)
}

func verifySignature(keys []jwkEntry, kid string, digest, sig []byte) bool {
	if kid != "" {
		for _, k := range keys {
			if k.kid == kid {
				return rsa.VerifyPKCS1v15(k.pub, crypto.SHA256, digest, sig) == nil
			}
		}
	}
	// Fallback: try all keys.
	for _, k := range keys {
		if rsa.VerifyPKCS1v15(k.pub, crypto.SHA256, digest, sig) == nil {
			return true
		}
	}
	return false
}

// rawPayload is the JWT payload as emitted by the auth service.
type rawPayload struct {
	Sub             string   `json:"sub"`
	Iss             string   `json:"iss"`
	Aud             jsonAud  `json:"aud"`
	Exp             float64  `json:"exp"`
	Nbf             float64  `json:"nbf"`
	PrincipalType   string   `json:"principal_type"`
	Email           string   `json:"email"`
	OrgID           string   `json:"org_id"`
	IsSuperadmin    bool     `json:"is_superadmin"`
	Scopes          []string `json:"scp"`
	Roles           []string `json:"roles"`
	ClientID        string   `json:"client_id"`
	Kind            string   `json:"kind"`
	ActorID         string   `json:"actor_id"`
	AuthorizedParty string   `json:"azp"`
}

// jsonAud handles the RFC 7519 "aud" claim which can be a string or []string.
type jsonAud []string

func (a *jsonAud) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*a = jsonAud{single}
		return nil
	}
	var multi []string
	if err := json.Unmarshal(data, &multi); err != nil {
		return err
	}
	*a = jsonAud(multi)
	return nil
}

func audMatch(tokenAud jsonAud, expected []string) bool {
	return slices.ContainsFunc(tokenAud, func(a string) bool {
		return slices.Contains(expected, a)
	})
}

// WriteUnauthorized writes the standard 401 JSON envelope
// ({"error":"unauthorized","message":<msg>}) used across Latere auth
// middleware. Centralised here so the client-facing wire contract has a single
// owner and cannot silently drift between packages.
func WriteUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized", "message": msg}); err != nil {
		// The envelope is a map[string]string, which cannot fail to marshal,
		// so this is a write failure: the client hung up between the status
		// line and the body. The response is already committed and there is no
		// second channel to report on, so record it and move on rather than
		// discard it -- a 401 that never reached the client is otherwise
		// indistinguishable from one that did.
		slog.Default().With("component", "jwtauth").Debug("write unauthorized body", "error", err)
	}
}
