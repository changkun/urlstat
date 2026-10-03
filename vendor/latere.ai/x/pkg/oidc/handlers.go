package oidc

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// ErrSessionExpired is returned by SessionFromRequest when the dashboard session
// lifetime has elapsed, or when the access token has expired and cannot be
// refreshed. The caller should clear the cookie and redirect to login, the same
// as for a decrypt error.
var ErrSessionExpired = errors.New("oidc: session expired")

// refreshLeeway is how far ahead of access-token expiry SessionFromRequest
// proactively refreshes, so a request never races the expiry boundary.
const refreshLeeway = 60 * time.Second

func accessTokenExpired(sess *Session, now time.Time) bool {
	return sess.Expiry.IsZero() || !sess.Expiry.After(now)
}

// sessionWindowElapsed reports whether the fixed dashboard-session lifetime
// (SessionExpiry) has lapsed. A zero SessionExpiry means no fixed window is in
// force (clients that leave Config.SessionTTL unset), so it never elapses.
// Shared by SessionFromRequest, UserFromRequest, and BuildMe so the three
// request-entry paths enforce the window identically.
func sessionWindowElapsed(sess *Session, now time.Time) bool {
	return !sess.SessionExpiry.IsZero() && !sess.SessionExpiry.After(now)
}

// jwtClaims holds the JWT access-token claims we surface in the session. These
// are identity hints for rendering — the access token itself remains the bearer
// of authority for downstream API calls, so the signature is not verified here
// (the token was just issued by our own auth service via a trusted exchange).
type jwtClaims struct {
	Sub             string   `json:"sub"`
	Email           string   `json:"email"`
	Name            string   `json:"name"`
	DisplayName     string   `json:"display_name"`
	Picture         string   `json:"picture"`
	AvatarURL       string   `json:"avatar_url"`
	OrgID           string   `json:"org_id"`
	ClientID        string   `json:"client_id"`
	AuthorizedParty string   `json:"azp"`
	Scope           string   `json:"scope"`
	Scopes          []string `json:"scopes"`
	// SCP is the fosite-standard scope claim ("scp"). It is checked
	// before Scope/Scopes by scopesFromJWT so tokens issued by fosite
	// (the issuer the auth service uses) surface granted scopes
	// correctly.
	SCP          []string `json:"scp"`
	Roles        []string `json:"roles"`
	IsSuperadmin bool     `json:"is_superadmin"`
}

// decodeJWTClaims extracts claims from a JWT access token without
// verifying the signature. The token was just issued by our own auth
// service via a trusted exchange, so verification is unnecessary.
func decodeJWTClaims(accessToken string) (*jwtClaims, error) {
	parts := strings.SplitN(accessToken, ".", 3)
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid JWT: expected 3 parts, got %d", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var c jwtClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// SessionFromToken builds a Session from a freshly issued token, decoding the
// access token's JWT for identity hints (sub, email, name, org, roles, scopes,
// client, superadmin). Identity hints in the cookie are UI-only; downstream API
// calls always re-validate the access token. SessionExpiry is stamped at now+ttl
// when ttl > 0 (the dashboard session lifetime), and left zero otherwise.
func SessionFromToken(token *oauth2.Token, ttl time.Duration) *Session {
	now := time.Now().UTC()
	exp := token.Expiry
	if exp.IsZero() {
		exp = now.Add(15 * time.Minute)
	}
	claims, _ := decodeJWTClaims(token.AccessToken)
	if claims == nil {
		claims = &jwtClaims{}
	}
	sess := &Session{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		Expiry:       exp.UTC(),
		User: User{
			Sub:          claims.Sub,
			Email:        claims.Email,
			Name:         claims.Name,
			Picture:      claims.Picture,
			AvatarURL:    cmp.Or(claims.AvatarURL, claims.Picture),
			OrgID:        claims.OrgID,
			DisplayName:  cmp.Or(claims.DisplayName, claims.Name),
			OrgRoles:     claims.Roles,
			ClientID:     cmp.Or(claims.ClientID, claims.AuthorizedParty),
			Scopes:       scopesFromJWT(claims),
			IsSuperadmin: claims.IsSuperadmin,
		},
	}
	// IssuedAt/SessionExpiry exist for the dashboard-session-lifetime feature
	// (SessionTTL > 0). Leaving them zero for the default config keeps the
	// serialized cookie identical to the pre-feature shape (the json omitzero
	// tag drops both), so existing relying-party cookies don't change.
	if ttl > 0 {
		sess.IssuedAt = now
		sess.SessionExpiry = now.Add(ttl)
	}
	return sess
}

// scopesFromJWT reads granted scopes from the fosite-standard "scp" array,
// the space-delimited "scope" claim, or the "scopes" array, normalizing to
// a deduped slice. "scp" wins because that is what the auth service (fosite)
// actually emits; the others are kept for tokens issued by other authorities.
func scopesFromJWT(c *jwtClaims) []string {
	if c == nil {
		return nil
	}
	if len(c.SCP) > 0 {
		return SplitScopes(strings.Join(c.SCP, " "))
	}
	if strings.TrimSpace(c.Scope) != "" {
		return SplitScopes(c.Scope)
	}
	if len(c.Scopes) > 0 {
		return SplitScopes(strings.Join(c.Scopes, " "))
	}
	return nil
}

// SplitScopes splits a space/comma-delimited scope string into a deduped,
// order-preserving slice. Exported so callers that load the same AUTH_SCOPES
// env-var format (e.g. authkit.LoadConfigWithPrefix) share one implementation.
func SplitScopes(s string) []string {
	parts := strings.Fields(strings.ReplaceAll(s, ",", " "))
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, p := range parts {
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// isSafeRedirect returns true when target is a relative path that won't
// redirect to an external host. Rejects "//host" (protocol-relative) and
// "/\host" (Chrome and other browsers normalize backslashes to forward
// slashes, so "/\evil.com" is followed as "//evil.com").
func isSafeRedirect(target string) bool {
	if target == "" || target[0] != '/' {
		return false
	}
	if len(target) == 1 {
		return true
	}
	return target[1] != '/' && target[1] != '\\'
}

// HandleLogin initiates the OAuth2 Authorization Code + PKCE flow.
// It generates a PKCE verifier and state, stores them in an encrypted
// flow cookie, and redirects the user to the auth service's authorize
// endpoint. The optional "return_to" query parameter is preserved so
// the callback can redirect the user back to their original page.
func (c *Client) HandleLogin(w http.ResponseWriter, r *http.Request) {
	verifier := GenerateVerifier()

	state, err := GenerateState()
	if err != nil {
		slog.Error("oidc: generate state", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	returnTo := r.URL.Query().Get("return_to")
	if !isSafeRedirect(returnTo) {
		returnTo = "/"
	}

	if err := c.SetFlowState(w, &FlowState{
		CodeVerifier: verifier,
		State:        state,
		ReturnTo:     returnTo,
	}); err != nil {
		slog.Error("oidc: set flow state", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Forward selected query params to the authorize endpoint as
	// extension parameters. Currently allowlisted: org_id, which the
	// auth service uses to scope the resulting token. New hints go
	// through this same list so unknown query strings don't leak into
	// the authorize URL accidentally.
	extra := forwardedAuthorizeParams(r.URL.Query())
	http.Redirect(w, r, c.AuthCodeURLWithOpts(state, verifier, extra), http.StatusFound)
}

// forwardedAuthorizeParams is the allowlist of query parameters on
// /login that get forwarded to the authorize endpoint. Kept narrow
// to avoid turning /login into an open pass-through.
//
// Presence-sensitive for org_id: a present-but-empty "org_id=" must
// survive the forward, because the auth service reads it as the
// clear-to-personal signal. An absent org_id leaves the session's
// active_org unchanged. Silently stripping the empty value is what
// caused "switch to Personal" to be a no-op end-to-end.
func forwardedAuthorizeParams(q url.Values) url.Values {
	out := url.Values{}
	for _, k := range []string{"org_id"} {
		if vs, ok := q[k]; ok {
			// Forward even when the value is empty, so the auth
			// service sees `?org_id=` and not the param missing.
			if len(vs) > 0 {
				out.Set(k, vs[0])
			} else {
				out.Set(k, "")
			}
		}
	}
	return out
}

// HandleCallback handles the OAuth2 redirect from the auth service.
// It validates the state, exchanges the authorization code for tokens,
// decodes the JWT claims, stores the session in an encrypted cookie,
// and redirects to the original return_to path.
func (c *Client) HandleCallback(w http.ResponseWriter, r *http.Request) {
	// Check for error from auth service.
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		desc := r.URL.Query().Get("error_description")
		slog.Warn("oidc: callback error", "error", errParam, "description", desc)
		http.Redirect(w, r, "/?auth_error="+url.QueryEscape(errParam), http.StatusFound)
		return
	}

	flow, err := c.GetFlowState(r)
	if err != nil {
		slog.Warn("oidc: get flow state", "error", err)
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	c.clearFlowState(w)

	// Verify state.
	if r.URL.Query().Get("state") != flow.State {
		slog.Warn("oidc: state mismatch")
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	// Exchange code for tokens.
	code := r.URL.Query().Get("code")
	token, err := c.Exchange(r, code, flow.CodeVerifier)
	if err != nil {
		slog.Error("oidc: token exchange", "error", err)
		http.Redirect(w, r, "/?auth_error=token_exchange_failed", http.StatusFound)
		return
	}

	// Validate the access token is a well-formed JWT before building a
	// session from it. The full claim set (sub/email/org/roles/scopes/...)
	// is decoded by SessionFromToken; name and picture not present in the
	// token are filled by a follow-up /userinfo round-trip in UserFromRequest.
	if _, err := decodeJWTClaims(token.AccessToken); err != nil {
		slog.Error("oidc: decode JWT claims", "error", err)
		http.Redirect(w, r, "/?auth_error=invalid_token", http.StatusFound)
		return
	}

	sess := SessionFromToken(token, c.cfg.SessionTTL)
	if err := c.SetSession(w, sess); err != nil {
		slog.Error("oidc: set session", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	returnTo := flow.ReturnTo
	if !isSafeRedirect(returnTo) {
		returnTo = "/"
	}
	http.Redirect(w, r, returnTo, http.StatusFound)
}

// HandleLogout clears the local session cookie and redirects to the
// auth service's logout endpoint. The optional "return_to" query
// parameter is forwarded as the post_logout_redirect_uri so the auth
// service can redirect the user back after sign-out.
func (c *Client) HandleLogout(w http.ResponseWriter, r *http.Request) {
	c.ClearSession(w)

	returnTo := r.URL.Query().Get("return_to")
	if !isSafeRedirect(returnTo) {
		returnTo = "/"
	}
	// Build an absolute URL so the auth service can redirect back. Behind a
	// TLS-terminating ingress r.TLS is nil, so trust
	// X-Forwarded-Proto first; fall back to r.TLS, then the localhost dev
	// heuristic.
	scheme := "https"
	if xfp := r.Header.Get("X-Forwarded-Proto"); xfp != "" {
		scheme = strings.TrimSpace(strings.Split(xfp, ",")[0])
	} else if r.TLS == nil && strings.HasPrefix(r.Host, "localhost") {
		scheme = "http"
	}
	postLogout := scheme + "://" + r.Host + returnTo
	http.Redirect(w, r, c.AuthURL()+"/logout?post_logout_redirect_uri="+url.QueryEscape(postLogout), http.StatusFound)
}

// HandleLogoutNotify is the front-channel logout endpoint: the auth service
// loads it in a hidden iframe when the user signs out elsewhere, so the local
// session cookie is cleared and the next page load reflects the logged-out
// state. It clears the session and returns 200.
func (c *Client) HandleLogoutNotify(w http.ResponseWriter, r *http.Request) {
	c.ClearSession(w)
	w.WriteHeader(http.StatusOK)
}

// UserFromRequest extracts the authenticated user from the session
// cookie. Returns nil if not authenticated or session is invalid.
// When an expired access token is successfully refreshed, the updated
// session is written back to the cookie via w so subsequent requests
// reuse the new token.
func (c *Client) UserFromRequest(w http.ResponseWriter, r *http.Request) *User {
	sess, err := c.GetSession(r)
	if err != nil {
		return nil
	}

	now := time.Now()
	// The fixed dashboard-session lifetime is independent of the access token:
	// once it lapses, clear the cookie even if the access token is still
	// refreshable, matching SessionFromRequest.
	if sessionWindowElapsed(sess, now) {
		c.ClearSession(w)
		return nil
	}

	// If the access token is expired, refresh it or clear the unusable session.
	if !c.refreshExpiredSession(w, r, sess, now) {
		return nil
	}

	// Decode sub + email from the JWT — available without a round-trip
	// and covers the common "tell me who this is" query. Name and
	// picture aren't in the access token; fetch them from /userinfo
	// so downstream RPs can render display_name / avatar_url without
	// knowing about a second endpoint. Userinfo failure falls back to
	// the JWT-only shape: better to return a partial User than nil.
	claims, err := decodeJWTClaims(sess.AccessToken)
	if err != nil {
		return nil
	}

	u := &User{
		Sub:   claims.Sub,
		Email: claims.Email,
		OrgID: claims.OrgID,
	}
	if info, err := c.FetchUserInfo(r, sess.AccessToken); err == nil && info != nil {
		// /userinfo is authoritative for profile fields; overwrite in
		// case the JWT copy is stale or missing.
		if info.Email != "" {
			u.Email = info.Email
		}
		u.Name = info.Name
		u.Picture = info.Picture
		u.AvatarURL = info.AvatarURL
		// auth's /userinfo refreshes org_id from the active SSO
		// session, so prefer it over the JWT copy when present.
		if info.OrgID != "" {
			u.OrgID = info.OrgID
		}
	} else if err != nil {
		// Don't fail the request (a partial User still renders), but make the
		// degradation visible: a 401 here means the access token lacks the
		// issuer audience, so name/avatar silently go missing. See the
		// audience-tagging contract in the auth service bootstrap.
		slog.Warn("oidc: /userinfo failed; display name + avatar will be missing",
			"error", err, "sub", u.Sub)
	}
	return u
}

// refreshExpiredSession refreshes sess's access token when it has expired,
// mutating sess in place and persisting the new token to the cookie via w. It
// reports whether the session is still usable: false (after clearing the
// cookie) when the token is expired and cannot be refreshed — no refresh token,
// or the refresh call failed — and true otherwise (including when the token was
// not expired and so left untouched). Shared by UserFromRequest and BuildMe so
// the two request-entry auth paths cannot drift on refresh/clear/persist.
func (c *Client) refreshExpiredSession(w http.ResponseWriter, r *http.Request, sess *Session, now time.Time) bool {
	if !accessTokenExpired(sess, now) {
		return true
	}
	if sess.RefreshToken == "" {
		c.ClearSession(w)
		return false
	}
	token, err := c.RefreshToken(r, sess.RefreshToken)
	if err != nil {
		slog.Debug("oidc: token refresh failed", "error", err)
		c.ClearSession(w)
		return false
	}
	sess.AccessToken = token.AccessToken
	sess.Expiry = token.Expiry
	if token.RefreshToken != "" {
		sess.RefreshToken = token.RefreshToken
	}
	// Persist the refreshed session so the next request doesn't need to refresh
	// again.
	if err := c.SetSession(w, sess); err != nil {
		slog.Warn("oidc: failed to persist refreshed session", "error", err)
	}
	return true
}

// SessionFromRequest decrypts the session cookie and returns the session,
// proactively refreshing the access token when it is within refreshLeeway of
// expiry. Unlike UserFromRequest it does NOT call /userinfo — cached identity
// fields persist from the prior session until the next login or an explicit
// /userinfo fetch — so it is cheap enough for the per-request auth path.
//
// Error contract (the caller must clear the cookie + redirect to login on any
// non-nil error, never surface a 500): a decrypt/parse failure returns the
// GetSession error unchanged; an elapsed SessionExpiry returns ErrSessionExpired.
// A successfully refreshed session is written back via w.
func (c *Client) SessionFromRequest(w http.ResponseWriter, r *http.Request) (*Session, error) {
	sess, err := c.GetSession(r)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if sessionWindowElapsed(sess, now) {
		return nil, ErrSessionExpired
	}

	if accessTokenExpired(sess, now) && sess.RefreshToken == "" {
		return nil, ErrSessionExpired
	}

	// Proactively refresh when the access token is within the leeway of expiry
	// (covers an already-expired token too). No refresh token means the current
	// access token is still usable until its expiry.
	if sess.RefreshToken == "" || !sess.Expiry.Add(-refreshLeeway).Before(now) {
		return sess, nil
	}

	tok, err := c.RefreshTokenContext(r.Context(), sess.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}

	refreshed := SessionFromToken(tok, c.cfg.SessionTTL)
	// Carry forward what the refresh response / new JWT may not restate.
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = sess.RefreshToken // some servers don't rotate
	}
	if !sess.SessionExpiry.IsZero() {
		refreshed.SessionExpiry = sess.SessionExpiry // never extend the window
	}
	if refreshed.User.ClientID == "" {
		refreshed.User.ClientID = sess.User.ClientID
	}
	if len(refreshed.User.Scopes) == 0 {
		refreshed.User.Scopes = sess.User.Scopes
	}
	if len(refreshed.User.OrgRoles) == 0 {
		refreshed.User.OrgRoles = sess.User.OrgRoles
	}
	// Profile fields aren't in the access token; keep the prior values rather
	// than blanking the header on refresh. Name/Picture are carried forward
	// alongside their DisplayName/AvatarURL aliases so a caller keying off
	// either name stays consistent across a silent refresh.
	refreshed.User.Name = cmp.Or(refreshed.User.Name, sess.User.Name)
	refreshed.User.Picture = cmp.Or(refreshed.User.Picture, sess.User.Picture)
	refreshed.User.DisplayName = cmp.Or(refreshed.User.DisplayName, sess.User.DisplayName)
	refreshed.User.AvatarURL = cmp.Or(refreshed.User.AvatarURL, sess.User.AvatarURL)
	refreshed.User.Email = cmp.Or(refreshed.User.Email, sess.User.Email)

	if err := c.SetSession(w, refreshed); err != nil {
		slog.Warn("oidc: failed to persist refreshed session", "error", err)
	}
	return refreshed, nil
}
