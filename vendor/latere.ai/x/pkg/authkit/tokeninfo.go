package authkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// TokenInfo is the subset of the auth service's GET /tokeninfo response that
// we consult when a consumer explicitly wants online revalidation. Field names
// match the issuing service's handler, which no longer reports any
// delegation fields.
type TokenInfo struct {
	Sub           string   `json:"sub"`
	PrincipalType string   `json:"principal_type"`
	Email         string   `json:"email,omitempty"`
	OrgID         string   `json:"org_id,omitempty"`
	Scopes        []string `json:"scopes"`
	Roles         []string `json:"roles"`
	ClientID      string   `json:"client_id,omitempty"`
}

// TokenInfoLookup is the online-validation contract. TokenInfoClient provides
// authoritative per-request lookups; CachedTokenInfo implements the same
// contract for explicitly read-only tiers. Consumers invoke it deliberately —
// no token claim triggers it on their behalf.
type TokenInfoLookup interface {
	Lookup(ctx context.Context, rawToken string) (*TokenInfo, error)
}

var (
	_ TokenInfoLookup = (*TokenInfoClient)(nil)
	_ TokenInfoLookup = (*CachedTokenInfo)(nil)
)

// TokenInfoClient calls GET {URL} with Authorization: Bearer <token>. It is
// stateless: every Lookup is an online call and the auth service is
// authoritative per request. Mutating requests MUST use this direct client.
// Read-tier consumers may wrap it in CachedTokenInfo (dr-21), which reuses a
// positive verdict for a short TTL — the documented trade being that a
// revoked delegation can keep READING for at most that window.
type TokenInfoClient struct {
	URL    string
	Client *http.Client
}

// NewTokenInfoClient creates a TokenInfoClient that calls url to validate
// strict agent tokens. Uses a 3-second HTTP timeout.
func NewTokenInfoClient(url string) *TokenInfoClient {
	return &TokenInfoClient{
		URL:    url,
		Client: &http.Client{Timeout: 3 * time.Second},
	}
}

// ErrRevoked is returned when the auth service signals the token is no
// longer valid — a 401 from /tokeninfo, or an empty response body.
var ErrRevoked = errors.New("token revoked or expired")

// Lookup calls the /tokeninfo endpoint with rawToken and returns the
// authoritative TokenInfo. Returns ErrRevoked if the token is no longer
// valid; wraps other errors from HTTP or JSON decoding.
func (c *TokenInfoClient) Lookup(ctx context.Context, rawToken string) (*TokenInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("tokeninfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+rawToken)
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tokeninfo http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrRevoked
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("tokeninfo status %d: %s", resp.StatusCode, body)
	}
	var ti TokenInfo
	if err := json.NewDecoder(resp.Body).Decode(&ti); err != nil {
		return nil, fmt.Errorf("tokeninfo decode: %w", err)
	}
	if ti.Sub == "" {
		return nil, ErrRevoked
	}
	return &ti, nil
}
