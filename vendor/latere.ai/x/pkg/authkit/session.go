package authkit

import (
	"net/http"
	"time"

	"latere.ai/x/pkg/oidc"
)

// SessionAuthenticator adapts an OIDC-managed encrypted session cookie to
// the Authenticator interface. It reads the cookie via oidc.Client.GetSession,
// trusts the (already-encrypted-and-MAC'd) embedded user fields, and stamps
// AuthMethod = MethodCookie.
//
// Construction with a nil Client yields an authenticator that always returns
// ErrUnauthenticated, so callers composing a Chain do not need a nil guard
// when cookie auth is optional.
type SessionAuthenticator struct {
	Client *oidc.Client
}

// NewSessionAuthenticator wires a cookie-session authenticator backed by c.
// c may be nil; see SessionAuthenticator for nil-handling semantics.
func NewSessionAuthenticator(c *oidc.Client) *SessionAuthenticator {
	return &SessionAuthenticator{Client: c}
}

func (s *SessionAuthenticator) Authenticate(r *http.Request) (Identity, error) {
	if s == nil || s.Client == nil {
		return Identity{}, ErrUnauthenticated
	}
	sess, err := s.Client.GetSession(r)
	if err != nil || sess == nil {
		return Identity{}, ErrUnauthenticated
	}
	now := time.Now()
	if sess.User.Sub == "" || sess.Expiry.IsZero() || !sess.Expiry.After(now) ||
		(!sess.SessionExpiry.IsZero() && !sess.SessionExpiry.After(now)) {
		return Identity{}, ErrUnauthenticated
	}
	return Identity{
		Sub:           sess.User.Sub,
		OrgID:         sess.User.OrgID,
		Email:         sess.User.Email,
		PrincipalType: "user",
		IsSuperadmin:  sess.User.IsSuperadmin,
		Scopes:        sess.User.Scopes,
		Roles:         sess.User.OrgRoles,
		ClientID:      sess.User.ClientID,
		TokenID:       sess.User.Sub,
		AuthMethod:    MethodCookie,
	}, nil
}
