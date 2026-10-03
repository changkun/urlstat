package authkit

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"latere.ai/x/pkg/jwtauth"
)

// validator is the subset of *jwtauth.Validator used by JWT, extracted as an
// interface so tests can inject a fake without running a real JWKS server.
type validator interface {
	Validate(rawToken string) (*jwtauth.Claims, error)
}

// JWT adapts latere.ai/x/pkg/jwtauth to the Authenticator interface. The
// underlying validator caches JWKS internally; we keep its Middleware out of
// the path so callers can apply their own request-id logging before auth.
//
// Validation is local: the signature and claims of the presented token decide
// the Identity. There is no longer a claim that makes this authenticator call
// /tokeninfo on its own. That tier existed for strict agent tokens, whose
// delegation could be revoked mid-lifetime. Agent delegation has since been
// removed and no surviving token has that property, so a service token's own
// short expiry is its revocation window.
//
// A consumer that still wants online revalidation calls TokenInfoClient
// explicitly, which is the honest shape: the decision belongs to the consumer,
// not to a claim on the token.
type JWT struct {
	V         validator
	TokenInfo TokenInfoLookup
}

// NewJWT wires a JWT authenticator. v is the JWKS-backed validator from
// latere.ai/x/pkg/jwtauth. ti is retained for source compatibility and is no
// longer consulted by Authenticate; pass nil unless you read j.TokenInfo
// yourself.
func NewJWT(v *jwtauth.Validator, ti TokenInfoLookup) *JWT {
	return &JWT{V: v, TokenInfo: ti}
}

func (j *JWT) Authenticate(r *http.Request) (Identity, error) {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if !strings.HasPrefix(h, p) {
		return Identity{}, ErrUnauthenticated
	}
	raw := h[len(p):]
	claims, err := j.V.Validate(raw)
	if err != nil {
		return Identity{}, err
	}
	// jwtauth.Claims surfaces the originating OAuth client (client_id, azp
	// fallback) from the signature-verified token — no second decode needed.
	clientID := claims.ClientID
	return Identity{
		Sub:           claims.Sub,
		OrgID:         claims.OrgID,
		Email:         claims.Email,
		PrincipalType: string(claims.PrincipalType),
		IsSuperadmin:  claims.IsSuperadmin,
		Scopes:        claims.Scopes,
		Roles:         claims.Roles,
		ClientID:      clientID,
		TokenID:       claims.Sub,
		Kind:          claims.Kind,
		ActorID:       claims.ActorID,
		AuthMethod:    MethodBearer,
	}, nil
}

func constantEq(a, b string) bool {
	// subtle.ConstantTimeCompare returns 0 on length mismatch, so the
	// length-leak property matches the previous hand-rolled check.
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
