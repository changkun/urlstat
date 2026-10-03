// Package authkit provides product-agnostic authentication glue for
// Latere AI services. It defines the shared Identity type and
// Authenticator interface so independent services share one
// implementation instead of maintaining drifting copies.
//
// Typical usage:
//
//	v := jwtauth.New(jwtauth.Config{JWKSURL: ..., Issuer: ...})
//	ti := authkit.NewTokenInfoClient(authURL + "/tokeninfo")
//	auth := authkit.NewJWT(v, ti)
//
//	mux.Handle("GET /api/resource", authkit.Middleware(handler, auth))
//
//	func handler(w http.ResponseWriter, r *http.Request) {
//	    id := authkit.IdentityFromContext(r.Context())
//	    // id.Sub, id.OrgID, id.Scopes ...
//	}
package authkit

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"latere.ai/x/pkg/jwtauth"
)

// Identity is what handlers see after authentication. Sub is the canonical
// owner key; OrgID is the tenant key. Resources created by a principal are
// labeled with both so lists and deletes can scope to them.
type Identity struct {
	Sub           string
	OrgID         string
	Email         string
	PrincipalType string // "user" | "service" | "agent" | "dev"
	IsSuperadmin  bool
	Scopes        []string
	// Roles are the caller's role names in the token's active org (e.g.
	// "owner", "admin", "member"), from the token's "roles" claim. They are
	// org-scoped: a personal-view token (no active org) carries none. A
	// consumer derives org authority from them (e.g. an org admin is a
	// holder of "owner" or "admin") without minting a product-specific
	// scope. Absent/unknown roles confer no authority (fail-safe).
	Roles []string
	// ClientID is the OAuth client_id of the caller's token. Used to
	// resolve per-client config. Empty for dev bearer tokens and for
	// older JWTs minted before the client_id claim was added.
	ClientID string
	// TokenID is a stable, low-cardinality audit identifier. For JWT it is
	// the principal Sub; for BearerToken it is "dev".
	TokenID string
	// Kind and ActorID identify a non-principal actor the token is bound
	// to (e.g. Kind == "sandbox", ActorID == the sandbox id). Generic —
	// consumers interpret ActorID according to Kind. Empty for ordinary
	// identities, and they do not affect tenancy: attribution stays
	// (OrgID, Sub).
	Kind    string
	ActorID string
	// AgentID is the acting agent's id, set by whichever Authenticator
	// resolved this Identity. It is a REPORTING and flow-gating dimension
	// only and does NOT affect tenancy (attribution stays (OrgID, Sub)).
	// Empty for ordinary identities.
	//
	// Its source is the authenticator's concern, not this package's. The
	// auth service no longer issues a delegated-agent JWT claim, so the
	// remaining producers set it from an agent-kind catalog token's own
	// binding claim.
	AgentID string
	// AuthMethod records which Authenticator resolved this Identity, for
	// observability and conditional handler logic. Standard values are
	// MethodBearer, MethodCookie, MethodStatic. Consumers may declare
	// additional AuthMethod values. The zero value ("") means "unspecified".
	AuthMethod AuthMethod
}

// AuthMethod is the discriminator stamped on Identity by an Authenticator to
// record how the request was authenticated. Built-in values are listed below;
// consumers composing custom Authenticators may declare their own.
type AuthMethod string

// Standard AuthMethod values stamped by built-in Authenticators.
const (
	MethodBearer AuthMethod = "bearer"
	MethodCookie AuthMethod = "cookie"
	MethodStatic AuthMethod = "static"
)

// Authenticator validates an inbound HTTP request and returns the caller's
// Identity.
type Authenticator interface {
	Authenticate(r *http.Request) (Identity, error)
}

// ErrUnauthenticated is returned when the request carries no recognisable
// credential.
var ErrUnauthenticated = errors.New("unauthenticated")

// ── Context helpers ──────────────────────────────────────────────────────────

type ctxKey int

const ctxKeyIdentity ctxKey = iota

// WithIdentity returns a copy of ctx with the given Identity attached.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKeyIdentity, id)
}

// IdentityFromContext extracts the Identity stored by WithIdentity. Returns
// a zero-value Identity when no identity is present.
func IdentityFromContext(ctx context.Context) Identity {
	id, _ := ctx.Value(ctxKeyIdentity).(Identity)
	return id
}

// ── Middleware ───────────────────────────────────────────────────────────────

// Middleware wraps next with authentication: it calls a.Authenticate,
// injects the Identity into the request context, and responds with 401 JSON
// on failure.
func Middleware(next http.Handler, a Authenticator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := a.Authenticate(r)
		if err != nil {
			// Log the detailed error server-side but return a generic body:
			// authenticator errors can wrap internal detail (tokeninfo HTTP
			// responses, backend topology) we must not disclose to clients.
			slog.Debug("authkit: authentication failed", "error", err)
			jwtauth.WriteUnauthorized(w, "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
	})
}

// ── Chain ────────────────────────────────────────────────────────────────────

// Chain tries each Authenticator in order and returns the first successful
// Identity. Useful when multiple credential formats circulate concurrently
// (e.g. during a token-type migration). An empty chain returns
// ErrUnauthenticated.
type Chain []Authenticator

func (c Chain) Authenticate(r *http.Request) (Identity, error) {
	lastErr := ErrUnauthenticated
	for _, a := range c {
		id, err := a.Authenticate(r)
		if err == nil {
			return id, nil
		}
		lastErr = err
	}
	return Identity{}, lastErr
}
