// Package oidc provides an OAuth 2.0 / OIDC Relying Party client for
// integrating Latere AI services with the auth service. It handles
// Authorization Code + PKCE flows, encrypted cookie-based sessions,
// token refresh, and userinfo fetching.
//
// Usage:
//
//	cfg := oidc.LoadConfig()
//	client := oidc.New(cfg)
//	if client == nil {
//	    // auth not configured, run without login
//	}
package oidc

import (
	"cmp"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"latere.ai/x/pkg/envutil"
)

const (
	SessionCookieName = "__Host-latere-session"
	FlowCookieName    = "__Host-latere-flow"
	SessionMaxAge     = 86400 // 24 hours
	FlowMaxAge        = 600   // 10 minutes
)

// User holds authenticated user info from the /userinfo endpoint.
//
// Picture and AvatarURL are aliases — auth's /userinfo emits the OIDC
// standard "picture" claim, but downstream callers historically referred
// to the URL as avatar_url. Populated together by FetchUserInfo so a
// caller can pick whichever name fits its template.
type User struct {
	Sub          string   `json:"sub"`
	Email        string   `json:"email"`
	Name         string   `json:"name"`
	Picture      string   `json:"picture"`
	OrgID        string   `json:"org_id,omitempty"`        // active org for this session, "" for personal view
	AvatarURL    string   `json:"avatar_url,omitempty"`    // alias of Picture
	DisplayName  string   `json:"display_name,omitempty"`  // preferred render name, falls back to Name
	OrgRoles     []string `json:"org_roles,omitempty"`     // roles in the active org (owner/admin/member/viewer)
	ClientID     string   `json:"client_id,omitempty"`     // oauth client the token was issued to (client_id or azp)
	Scopes       []string `json:"scopes,omitempty"`        // granted scopes parsed from the access token
	IsSuperadmin bool     `json:"is_superadmin,omitempty"` // mirrored from the JWT at login
}

// Session holds tokens and user info stored in the encrypted session cookie.
//
// Expiry is the access-token expiry. SessionExpiry is the (longer) dashboard
// session lifetime — distinct from the access token, so the server can keep
// reading the refresh token to renew access tokens until the session itself
// lapses. SessionExpiry is only populated when the client sets Config.SessionTTL;
// otherwise the cookie's MaxAge tracks SessionMaxAge as before.
type Session struct {
	AccessToken   string    `json:"at"`
	RefreshToken  string    `json:"rt"`
	Expiry        time.Time `json:"exp"`
	User          User      `json:"u"`
	IssuedAt      time.Time `json:"iat,omitzero"`  // access-token issued-at (only when SessionTTL is set)
	SessionExpiry time.Time `json:"sexp,omitzero"` // dashboard session expiry, distinct from Expiry
}

// FlowState holds PKCE and state params during the OAuth authorization flow.
type FlowState struct {
	CodeVerifier string `json:"cv"`
	State        string `json:"st"`
	ReturnTo     string `json:"rt"`
}

// Config holds auth integration configuration.
type Config struct {
	AuthURL      string // base URL of the auth service, e.g. https://auth.latere.ai
	ClientID     string
	ClientSecret string
	RedirectURL  string // callback URL, e.g. https://app.latere.ai/callback
	CookieKey    string // encryption key for cookies (hex or raw string)

	// Audience requested on /authorize. Defaults to AuthURL when empty,
	// which is what the auth service requires for its JWT-protected
	// endpoints (/me/orgs, /userinfo, /tokeninfo). Set explicitly only
	// when the issued access token is meant for a different relying
	// party (e.g. tokens with aud="<other-service>").
	Audience string

	// Scopes requested on /authorize. When empty, defaults to
	// ["openid", "email", "profile"] — the minimum needed for the
	// /userinfo claims this package surfaces. Set explicitly to
	// request additional scopes (offline_access for refresh tokens,
	// or product-specific scopes the relying party gates on).
	Scopes []string

	// CookieName overrides the session cookie name. Defaults to
	// SessionCookieName ("__Host-latere-session"). Set this only when a
	// relying party needs a non-default name; note the "__Host-" prefix
	// requires Secure cookies.
	CookieName string

	// SessionTTL sets the dashboard session lifetime. When zero (the
	// default), the session cookie's MaxAge tracks SessionMaxAge and no
	// SessionExpiry is stamped — byte-for-byte the legacy behavior. When
	// positive, the cookie MaxAge derives from a SessionExpiry stamped at
	// now+SessionTTL, so access tokens can be refreshed server-side until
	// the longer session expires.
	SessionTTL time.Duration

	// InsecureCookies drops the Secure flag from cookies, for local/dev
	// over plain HTTP. Defaults false (Secure stays set). Incompatible with
	// a "__Host-" cookie name, which browsers reject without Secure.
	InsecureCookies bool
}

// Client is the OIDC Relying Party for a latere-ai service.
type Client struct {
	cfg       Config
	oauthCfg  oauth2.Config
	cookieKey [32]byte
}

// LoadConfig reads auth configuration from environment variables.
//
// AUTH_INSECURE_COOKIES drops the Secure flag (and lets a non-"__Host-" cookie
// name work) for local development of a service pointed at a remote IDP over
// plain HTTP. It must never be set in production.
func LoadConfig() Config {
	return Config{
		AuthURL:         cmp.Or(os.Getenv("AUTH_URL"), "https://auth.latere.ai"),
		ClientID:        os.Getenv("AUTH_CLIENT_ID"),
		ClientSecret:    os.Getenv("AUTH_CLIENT_SECRET"),
		RedirectURL:     os.Getenv("AUTH_REDIRECT_URL"),
		CookieKey:       os.Getenv("AUTH_COOKIE_KEY"),
		Audience:        os.Getenv("AUTH_AUDIENCE"),
		Scopes:          SplitScopes(os.Getenv("AUTH_SCOPES")),
		InsecureCookies: envutil.IsTruthy(os.Getenv("AUTH_INSECURE_COOKIES")),
	}
}

// Enabled returns true if the required configuration is present.
//
// ClientID is always required. The browser-based authorization-code + PKCE
// flow needs a RedirectURL; the client secret is OPTIONAL because PKCE (S256)
// secures the code exchange — a public client sends no secret, a confidential
// client sends one when present. A client with neither RedirectURL nor secret
// is device-code-only (RFC 8628): DeviceAuth and DeviceAccessToken work without
// either; the cookie helpers and HandleLogin fail loudly at use time if called
// against such a client.
func (c Config) Enabled() bool {
	return c.ClientID != ""
}

// New creates a new OIDC Client. Returns nil if the config is not enabled, or
// if a browser-mode public client (no client secret) is configured without an
// AUTH_COOKIE_KEY — see the cookie-key handling below.
func New(cfg Config) *Client {
	if !cfg.Enabled() {
		slog.Info("oidc: disabled (AUTH_CLIENT_ID not set)")
		return nil
	}

	if cfg.Audience == "" {
		cfg.Audience = cfg.AuthURL
	}
	if cfg.CookieName == "" {
		cfg.CookieName = SessionCookieName
	}
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{"openid", "email", "profile"}
	}

	// Public clients (no secret) authenticate to the token endpoint by sending
	// client_id in the request body and no Authorization header. AuthStyleInHeader
	// would emit "Basic base64(client_id:)" — an empty-secret client_secret_basic
	// attempt the auth server treats as a (failed) confidential auth. Confidential
	// clients keep client_secret_basic (set auth style explicitly, never
	// AuthStyleAutoDetect — see INTEGRATION.md).
	authStyle := oauth2.AuthStyleInHeader
	if cfg.ClientSecret == "" {
		authStyle = oauth2.AuthStyleInParams
	}

	c := &Client{
		cfg: cfg,
		oauthCfg: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint: oauth2.Endpoint{
				AuthURL:       cfg.AuthURL + "/authorize",
				TokenURL:      cfg.AuthURL + "/token",
				DeviceAuthURL: cfg.AuthURL + "/device/code",
				AuthStyle:     authStyle,
			},
			Scopes: scopes,
		},
	}

	// Cookie key + startup log only matter for relying parties using
	// the browser-based session helpers; a device-only client (no
	// RedirectURL) skips both so a CLI invocation doesn't dump a
	// warning + info line on every run.
	browserMode := cfg.RedirectURL != ""
	if browserMode {
		switch {
		case cfg.CookieKey != "":
			if key, err := hex.DecodeString(cfg.CookieKey); err == nil && len(key) >= 16 {
				c.cookieKey = sha256.Sum256(key)
			} else {
				c.cookieKey = sha256.Sum256([]byte(cfg.CookieKey))
			}
		case cfg.ClientSecret != "":
			// Confidential client without an explicit cookie key: derive from the
			// secret for backward compatibility, but warn — AUTH_COOKIE_KEY should
			// be set explicitly.
			slog.Warn("oidc: AUTH_COOKIE_KEY not set, deriving cookie key from client secret — set AUTH_COOKIE_KEY explicitly")
			c.cookieKey = sha256.Sum256([]byte(cfg.ClientSecret))
		default:
			// Public client with no cookie key: refuse. There is no secret to
			// derive from, and proceeding would encrypt every session cookie
			// under sha256("") — a fixed, publicly-known key. Fail closed.
			slog.Error("oidc: browser mode requires AUTH_COOKIE_KEY for a public client (no client secret to derive a key from)")
			return nil
		}
		slog.Info("oidc: enabled", "auth_url", cfg.AuthURL, "client_id", cfg.ClientID, "public", cfg.ClientSecret == "")
	}
	return c
}

// AuthURL returns the auth service base URL.
func (c *Client) AuthURL() string {
	return c.cfg.AuthURL
}

// --- PKCE and state ---

// randReader is the entropy source. Package-level variable for testability.
var randReader io.Reader = rand.Reader

// GenerateVerifier creates a PKCE code verifier.
// Uses the oauth2 package's built-in generator for correct formatting.
func GenerateVerifier() string {
	return oauth2.GenerateVerifier()
}

// GenerateState creates a random state parameter for CSRF protection.
func GenerateState() (string, error) {
	buf := make([]byte, 16)
	if _, err := io.ReadFull(randReader, buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// --- OAuth2 operations ---

// AuthCodeURL returns the URL to redirect the user to for authorization.
// The verifier is used to derive the S256 challenge automatically.
func (c *Client) AuthCodeURL(state, verifier string) string {
	return c.AuthCodeURLWithOpts(state, verifier, nil)
}

// AuthCodeURLWithOpts returns the authorize URL with additional
// extension parameters appended as-is. Use for non-standard hints the
// auth service understands — e.g. org_id to scope the resulting token
// to a specific organization, or login_hint to preselect an identity
// provider. Standard OAuth parameters (response_type, scope, state,
// code_challenge, redirect_uri) are already handled; do not pass them
// here.
//
// Each value becomes a single oauth2.SetAuthURLParam so unknown keys
// round-trip through to the auth server unchanged.
func (c *Client) AuthCodeURLWithOpts(state, verifier string, extra url.Values) string {
	opts := []oauth2.AuthCodeOption{
		oauth2.S256ChallengeOption(verifier),
	}
	// Stamp the audience on every authorize URL. Without it, fosite
	// emits aud:[] in the access token (the JWT strategy always
	// materialises the claim from the granted audience set, even when
	// empty), and the auth service's JWT validator then rejects every
	// JWT-protected endpoint as an audience mismatch.
	if c.cfg.Audience != "" {
		opts = append(opts, oauth2.SetAuthURLParam("audience", c.cfg.Audience))
	}
	opts = append(opts, authURLParams(extra)...)
	return c.oauthCfg.AuthCodeURL(state, opts...)
}

// authURLParams converts extra query values into oauth2 auth-URL options. It
// forwards every present key, including empty-string values: callers rely on
// passing signals like org_id="" which the auth service interprets as "clear
// the active org" — silently dropping it would turn the switch-to-personal UX
// into a no-op. Shared by AuthCodeURLWithOpts and DeviceAuth so the two flows
// cannot drift on this presence-sensitive handling.
func authURLParams(extra url.Values) []oauth2.AuthCodeOption {
	var opts []oauth2.AuthCodeOption
	for k, vs := range extra {
		if len(vs) == 0 {
			opts = append(opts, oauth2.SetAuthURLParam(k, ""))
			continue
		}
		for _, v := range vs {
			opts = append(opts, oauth2.SetAuthURLParam(k, v))
		}
	}
	return opts
}

// Exchange trades an authorization code for tokens using the PKCE verifier.
func (c *Client) Exchange(r *http.Request, code, verifier string) (*oauth2.Token, error) {
	return c.ExchangeContext(r.Context(), code, verifier)
}

// ExchangeContext is the context-only form of Exchange. Prefer this in
// non-HTTP-handler contexts (background workers, internal auth bridges)
// where threading a *http.Request just to pass its Context is awkward.
func (c *Client) ExchangeContext(ctx context.Context, code, verifier string) (*oauth2.Token, error) {
	return c.oauthCfg.Exchange(ctx, code,
		oauth2.VerifierOption(verifier),
	)
}

// FetchUserInfo calls the auth service /userinfo endpoint.
//
// Auth emits the OIDC-standard "picture" claim; mirror it onto
// User.AvatarURL so callers that key off avatar_url don't have to
// reach for the Picture field.
func (c *Client) FetchUserInfo(r *http.Request, accessToken string) (*User, error) {
	return c.FetchUserInfoContext(r.Context(), accessToken)
}

// FetchUserInfoContext is the context-only form of FetchUserInfo.
// Same response handling; pick this when threading r is awkward.
func (c *Client) FetchUserInfoContext(ctx context.Context, accessToken string) (*User, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.cfg.AuthURL+"/userinfo", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := httpDo(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo returned %d", resp.StatusCode)
	}

	var u User
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, fmt.Errorf("decode userinfo: %w", err)
	}
	if u.AvatarURL == "" {
		u.AvatarURL = u.Picture
	}
	return &u, nil
}

// DeviceAuth initiates an RFC 8628 device authorization flow against
// the configured AuthURL. The caller renders the returned UserCode +
// VerificationURI to the user, then calls DeviceAccessToken to poll
// for approval. extra carries auth-server extension parameters
// (auth.latere.ai honours `org_id` to scope the resulting token).
//
// Use this for headless / CLI flows. Browser-based clients should
// use HandleLogin instead.
func (c *Client) DeviceAuth(ctx context.Context, extra url.Values) (*oauth2.DeviceAuthResponse, error) {
	return c.oauthCfg.DeviceAuth(ctx, authURLParams(extra)...)
}

// DeviceAccessToken polls the token endpoint with the device-code
// grant until the user approves, denies, or the code expires. RFC
// 8628 slow_down / authorization_pending semantics are honoured by
// the underlying oauth2 client.
func (c *Client) DeviceAccessToken(ctx context.Context, da *oauth2.DeviceAuthResponse) (*oauth2.Token, error) {
	return c.oauthCfg.DeviceAccessToken(ctx, da)
}

// RefreshToken uses a refresh token to obtain a new access token.
func (c *Client) RefreshToken(r *http.Request, refreshToken string) (*oauth2.Token, error) {
	return c.RefreshTokenContext(r.Context(), refreshToken)
}

// RefreshTokenContext is the context-only form of RefreshToken.
// Same flow; pick this when threading r is awkward.
func (c *Client) RefreshTokenContext(ctx context.Context, refreshToken string) (*oauth2.Token, error) {
	ts := c.oauthCfg.TokenSource(ctx, &oauth2.Token{
		RefreshToken: refreshToken,
	})
	return ts.Token()
}

// httpDo is a package-level variable for testability.
var httpDo = func(req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}

// --- Cookie helpers ---

// SetFlowState encrypts and writes the flow cookie.
func (c *Client) SetFlowState(w http.ResponseWriter, state *FlowState) error {
	return c.setCookie(w, FlowCookieName, state, FlowMaxAge)
}

// GetFlowState reads and decrypts the flow cookie.
func (c *Client) GetFlowState(r *http.Request) (*FlowState, error) {
	var state FlowState
	if err := c.getCookie(r, FlowCookieName, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// ClearFlowState expires the flow cookie.
func ClearFlowState(w http.ResponseWriter) {
	clearCookie(w, FlowCookieName, true)
}

// SetSession encrypts and writes the session cookie.
//
// With the default config (SessionTTL == 0) the cookie name is
// SessionCookieName and MaxAge is SessionMaxAge — unchanged from before. When
// SessionTTL is set, the cookie name is c.cfg.CookieName and MaxAge derives
// from the session's SessionExpiry (stamped here if absent, preserved if
// already set so a refresh never extends the window).
func (c *Client) SetSession(w http.ResponseWriter, sess *Session) error {
	maxAge := SessionMaxAge
	if c.cfg.SessionTTL > 0 {
		now := time.Now().UTC()
		exp := c.sessionExpiry(sess, now)
		sess.SessionExpiry = exp
		maxAge = max(int(exp.Sub(now).Seconds()), 1)
	}
	return c.setCookie(w, c.cfg.CookieName, sess, maxAge)
}

// sessionExpiry returns the dashboard session expiry for sess, preserving an
// already-stamped value so refreshes don't extend the window. Legacy sessions
// without one are bounded by their IssuedAt (or now) plus SessionTTL.
func (c *Client) sessionExpiry(sess *Session, now time.Time) time.Time {
	if !sess.SessionExpiry.IsZero() {
		return sess.SessionExpiry
	}
	base := sess.IssuedAt
	if base.IsZero() {
		base = now
	}
	return base.Add(c.cfg.SessionTTL)
}

// GetSession reads and decrypts the session cookie at the client's
// configured cookie name.
func (c *Client) GetSession(r *http.Request) (*Session, error) {
	return c.GetSessionByName(r, c.cfg.CookieName)
}

// GetSessionByName reads and decrypts the session cookie at the given name,
// ignoring the client's configured CookieName. Intended for cookie-name
// migrations: a relying party can try GetSession first and fall back to
// GetSessionByName for one or more legacy names during the cutover window,
// without constructing a second *Client.
func (c *Client) GetSessionByName(r *http.Request, name string) (*Session, error) {
	var sess Session
	if err := c.getCookie(r, name, &sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

// ClearSession expires the default session cookie (__Host-latere-session, with
// Secure set). This package-level form is the public API for relying parties
// that clear a session without holding a *Client, such as an org-switch
// handler or a re-exported logout route. Clients configured with a custom
// cookie name must use the method below.
func ClearSession(w http.ResponseWriter) {
	clearCookie(w, SessionCookieName, true)
}

// ClearSession expires the session cookie using the client's configured cookie
// name and Secure setting, the form a relying party with a custom CookieName
// (e.g. "__myapp_session") must use.
func (c *Client) ClearSession(w http.ResponseWriter) {
	clearCookie(w, c.effectiveCookieName(c.cfg.CookieName), !c.cfg.InsecureCookies)
}

// clearFlowState expires the flow cookie honoring the client's InsecureCookies
// setting, the method-based companion to the package-level ClearFlowState. The
// browser flow over plain HTTP renames the cookie (see effectiveCookieName), so
// the callback must clear the same name it set.
func (c *Client) clearFlowState(w http.ResponseWriter) {
	clearCookie(w, c.effectiveCookieName(FlowCookieName), !c.cfg.InsecureCookies)
}

// effectiveCookieName drops the "__Host-"/"__Secure-" prefix when cookies are
// served insecurely (plain HTTP, local development). Browsers reject a cookie
// carrying those prefixes unless the Secure attribute is set, so a prefixed
// name under InsecureCookies would be silently discarded and break the flow.
func (c *Client) effectiveCookieName(name string) string {
	if !c.cfg.InsecureCookies {
		return name
	}
	name = strings.TrimPrefix(name, "__Host-")
	name = strings.TrimPrefix(name, "__Secure-")
	return name
}

// errNoCookieKey is returned by the cookie helpers when the client's cookie key
// was never derived (a device-only client built without a RedirectURL leaves it
// the all-zero array). Sealing a session under a fixed, publicly-known key must
// fail closed rather than mint a forgeable cookie.
var errNoCookieKey = errors.New("oidc: cookie helpers require a configured cookie key (client built without a RedirectURL)")

// cookieKeyConfigured reports whether a real cookie key was derived. A
// sha256-derived key is never all-zero, so the zero array unambiguously means
// "never configured".
func (c *Client) cookieKeyConfigured() bool {
	return c.cookieKey != [32]byte{}
}

func (c *Client) setCookie(w http.ResponseWriter, name string, v any, maxAge int) error {
	if !c.cookieKeyConfigured() {
		return errNoCookieKey
	}
	name = c.effectiveCookieName(name)
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal cookie %s: %w", name, err)
	}

	ciphertext, err := aesGCMEncrypt(c.cookieKey[:], data)
	if err != nil {
		return fmt.Errorf("encrypt cookie %s: %w", name, err)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    base64.RawURLEncoding.EncodeToString(ciphertext),
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   !c.cfg.InsecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (c *Client) getCookie(r *http.Request, name string, v any) error {
	if !c.cookieKeyConfigured() {
		return errNoCookieKey
	}
	name = c.effectiveCookieName(name)
	cookie, err := r.Cookie(name)
	if err != nil {
		if errors.Is(err, http.ErrNoCookie) {
			return fmt.Errorf("cookie %s not found", name)
		}
		return fmt.Errorf("read cookie %s: %w", name, err)
	}

	ciphertext, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return fmt.Errorf("decode cookie %s: %w", name, err)
	}

	plaintext, err := aesGCMDecrypt(c.cookieKey[:], ciphertext)
	if err != nil {
		return fmt.Errorf("decrypt cookie %s: %w", name, err)
	}

	if err := json.Unmarshal(plaintext, v); err != nil {
		return fmt.Errorf("unmarshal cookie %s: %w", name, err)
	}
	return nil
}

func clearCookie(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// --- AES-GCM (same pattern as auth service) ---

// Package-level function variables for testability (see otel package).
var aesGCMEncrypt = defaultAESGCMEncrypt

func defaultAESGCMEncrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(randReader, nonce); err != nil {
		return nil, err
	}

	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func aesGCMDecrypt(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ct := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return gcm.Open(nil, nonce, ct, nil)
}
