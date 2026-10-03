package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OrgEntry mirrors the auth service's /me/orgs payload entry. Keep
// this shape aligned with the auth API so RPs that type-share don't
// have to redeclare it.
type OrgEntry struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Slug  string `json:"slug,omitempty"`
	Owner bool   `json:"owner,omitempty"`
}

// FetchOrgs calls GET /me/orgs on the configured AuthURL with the
// supplied access token and returns the org list. The endpoint
// requires a valid JWT; tokens issued without Config.Audience set to
// the AuthURL surface here as a 401 rather than an empty list, so
// callers see the misconfiguration instead of an unexplained empty
// switcher.
func (c *Client) FetchOrgs(ctx context.Context, accessToken string) ([]OrgEntry, error) {
	if c == nil {
		return nil, errors.New("oidc: client is nil")
	}
	url := strings.TrimRight(c.cfg.AuthURL, "/") + "/me/orgs"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := httpDo(req)
	if err != nil {
		return nil, fmt.Errorf("call /me/orgs: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("/me/orgs %d: %s", resp.StatusCode, body)
	}

	var out []OrgEntry
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode /me/orgs: %w", err)
	}
	return out, nil
}
