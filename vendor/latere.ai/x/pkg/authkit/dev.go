package authkit

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"latere.ai/x/pkg/envutil"
	"latere.ai/x/pkg/oidc"
)

// MethodDev marks an Identity synthesized by the dev-bypass authenticator.
const MethodDev AuthMethod = "dev"

// DevAuthenticator bypasses real authentication and resolves EVERY request to a
// single fixed, synthetic Identity. It exists only for local development of a
// service that should run with no IDP dependency.
//
// It is dangerous by nature: if it ever ships enabled to production every
// request becomes the synthetic user. The safety model is therefore layered and
// fail-closed:
//
//   - OFF by default. DevAuthenticatorFromEnv returns (nil, nil) unless
//     AUTH_DEV_BYPASS is set, so a disabled bypass is absent from the auth
//     chain, not a present-but-inert member one bug away from returning an
//     identity.
//   - Default-deny posture guard (an allowlist of dev, not a denylist of prod):
//     NewDevAuthenticator activates only when the deployment host is loopback,
//     OR the explicit AUTH_DEV_BYPASS_INSECURE override is set. Any unknown,
//     empty, or non-loopback host fails CLOSED.
//   - Non-superadmin by default. The synthetic identity is a plain user unless
//     AUTH_DEV_SUPERADMIN is explicitly set, so a leak is contained.
//
// As defence-in-depth a deployment may additionally keep this file behind a
// build tag in release builds; the runtime gate above is the primary control.
type DevAuthenticator struct {
	id Identity
}

// DevConfig configures a DevAuthenticator.
type DevConfig struct {
	Subject      string   // synthetic principal id (Identity.Sub)
	Email        string   // synthetic email
	Org          string   // synthetic org id (Identity.OrgID)
	Scopes       []string // granted scopes
	IsSuperadmin bool     // admin opt-in; default false keeps a leak contained

	// PostureHost is the host the deployment serves itself on — derived from a
	// value the service actually holds (its redirect URL or issuer host), NOT a
	// production marker. The guard activates the bypass only when this is a
	// loopback host, unless Insecure is true.
	PostureHost string
	// Insecure is the explicit second gate that permits the bypass on a
	// non-loopback host (e.g. a shared dev/staging box). Never set in production.
	Insecure bool
}

// NewDevAuthenticator builds a dev-bypass authenticator, enforcing the
// default-deny posture guard. It returns an error when the bypass would activate
// on a non-loopback host without the Insecure override — including when the host
// is empty or unparseable, which fail closed.
func NewDevAuthenticator(cfg DevConfig) (*DevAuthenticator, error) {
	if !cfg.Insecure && !isLoopbackHost(cfg.PostureHost) {
		return nil, fmt.Errorf(
			"authkit: dev bypass refused for non-loopback host %q — set AUTH_DEV_BYPASS_INSECURE=true to override (never in production)",
			cfg.PostureHost)
	}
	sub := cfg.Subject
	if sub == "" {
		sub = "dev-local"
	}
	id := Identity{
		Sub:           sub,
		OrgID:         cfg.Org,
		Email:         cfg.Email,
		PrincipalType: "dev",
		IsSuperadmin:  cfg.IsSuperadmin,
		Scopes:        cfg.Scopes,
		ClientID:      "",
		TokenID:       "dev",
		AuthMethod:    MethodDev,
	}
	slog.Warn("authkit: DEV BYPASS active — every request runs as the synthetic dev identity",
		"sub", sub, "org", cfg.Org, "superadmin", cfg.IsSuperadmin, "insecure", cfg.Insecure)
	return &DevAuthenticator{id: id}, nil
}

// DevAuthenticatorFromEnv constructs a DevAuthenticator from environment
// variables, or returns (nil, nil) when AUTH_DEV_BYPASS is not set. Callers
// append the result to their Chain only when it is non-nil:
//
//	dev, err := authkit.DevAuthenticatorFromEnv()
//	if err != nil { log.Fatal(err) }
//	chain := authkit.Chain{jwtAuth}
//	if dev != nil { chain = authkit.Chain{dev} }
//
// Env vars:
//
//	AUTH_DEV_BYPASS           enable when "true"/"1"
//	AUTH_DEV_BYPASS_INSECURE  permit on a non-loopback host
//	AUTH_DEV_SUBJECT          synthetic Sub (default "dev-local")
//	AUTH_DEV_EMAIL            synthetic email
//	AUTH_DEV_ORG              synthetic org id
//	AUTH_DEV_SCOPES           comma/space-separated granted scopes
//	AUTH_DEV_SUPERADMIN       grant superadmin when "true"/"1" (default false)
//
// The posture host is taken from AUTH_REDIRECT_URL (preferred) or AUTH_URL.
func DevAuthenticatorFromEnv() (*DevAuthenticator, error) {
	if !envutil.IsTruthy(os.Getenv("AUTH_DEV_BYPASS")) {
		return nil, nil
	}
	return NewDevAuthenticator(DevConfig{
		Subject:      os.Getenv("AUTH_DEV_SUBJECT"),
		Email:        os.Getenv("AUTH_DEV_EMAIL"),
		Org:          os.Getenv("AUTH_DEV_ORG"),
		Scopes:       oidc.SplitScopes(os.Getenv("AUTH_DEV_SCOPES")),
		IsSuperadmin: envutil.IsTruthy(os.Getenv("AUTH_DEV_SUPERADMIN")),
		PostureHost:  postureHostFromEnv(),
		Insecure:     envutil.IsTruthy(os.Getenv("AUTH_DEV_BYPASS_INSECURE")),
	})
}

// Authenticate returns the fixed synthetic identity for every request. A nil
// receiver (bypass not configured) returns ErrUnauthenticated so an
// accidentally-wired nil never authenticates.
func (d *DevAuthenticator) Authenticate(*http.Request) (Identity, error) {
	if d == nil {
		return Identity{}, ErrUnauthenticated
	}
	return d.id, nil
}

// postureHostFromEnv derives the deployment host from the redirect URL (a
// browser RP holds it) or the issuer URL, returning "" when neither is set so
// the guard fails closed.
func postureHostFromEnv() string {
	for _, v := range []string{os.Getenv("AUTH_REDIRECT_URL"), os.Getenv("AUTH_URL")} {
		if v == "" {
			continue
		}
		if u, err := url.Parse(v); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return ""
}

// isLoopbackHost reports whether host (optionally with a port) is a loopback
// address — localhost, 127.0.0.0/8, or ::1. Empty or unparseable input is NOT
// loopback, so the posture guard fails closed.
func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
