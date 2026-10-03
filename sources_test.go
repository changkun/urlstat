// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSourceOf(t *testing.T) {
	for _, tt := range []struct {
		kind, in, want string
		ok             bool
	}{
		{kindSite, "https://changkun.de", "changkun.de", true},
		{kindSite, "https://Changkun.de/bobook/en/?x=1#top", "changkun.de", true},
		{kindSite, " example.com ", "example.com", true},
		{kindSite, "http://example.com:8080/path", "example.com:8080", true},
		{kindSite, "localhost", "localhost", true},
		{kindSite, "", "", false},
		{kindSite, "https://", "", false},
		{kindSite, "exa mple.com", "", false},
		{kindSite, "user@example.com", "", false},
		{kindSite, "example.com:port", "", false},
		{kindSite, "-example.com", "", false},
		{kindGitHub, "changkun", "changkun", true},
		{kindGitHub, "@Changkun", "changkun", true},
		{kindGitHub, "https://github.com/golang-design/research", "golang-design", true},
		{kindGitHub, "changkun/redir", "changkun", true},
		{kindGitHub, "not a name", "", false},
		{kindGitHub, "-dash", "", false},
		{"other", "changkun.de", "", false},
	} {
		if got, ok := sourceOf(tt.kind, tt.in); got != tt.want || ok != tt.ok {
			t.Errorf("sourceOf(%q, %q) = %q, %v; want %q, %v", tt.kind, tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

// TestSourcesAllow pins that a source is matched whole. The list used to be
// matched as a substring, so a host that merely began with an allowed one
// was counted as it.
func TestSourcesAllow(t *testing.T) {
	s := &sourceList{sites: map[string]bool{"changkun.de": true}, github: map[string]bool{"changkun": true}, refused: map[string]*refusal{}}
	for origin, want := range map[string]bool{
		"https://changkun.de":             true,
		"http://changkun.de":              true,
		"https://CHANGKUN.de/":            true,
		"https://changkun.de.example.com": false,
		"https://changkun.de:8443":        false,
		"https://www.changkun.de":         false,
		"https://notchangkun.de":          false,
		"http://localhost:8080":           false,
		"":                                false,
	} {
		if got := s.allowsOrigin(origin); got != want {
			t.Errorf("allowsOrigin(%q) = %v, want %v", origin, got, want)
		}
	}
	for account, want := range map[string]bool{"changkun": true, "Changkun": true, "changkun-other": false, "chang": false, "": false} {
		if got := s.allowsGitHub(account); got != want {
			t.Errorf("allowsGitHub(%q) = %v, want %v", account, got, want)
		}
	}

	// Outside production a page on this machine may report, on any port.
	s.dev = true
	for origin, want := range map[string]bool{"http://localhost:8080": true, "http://127.0.0.1": true, "http://localhost.example.com": false} {
		if got := s.allowsOrigin(origin); got != want {
			t.Errorf("in development, allowsOrigin(%q) = %v, want %v", origin, got, want)
		}
	}
}

// TestRefusals checks that who was turned away is counted, that nonsense is
// not remembered, and that the memory is bounded.
func TestRefusals(t *testing.T) {
	s := &sourceList{refused: map[string]*refusal{}}
	s.refuse(kindSite, "https://Example.org/page")
	s.refuse(kindSite, "http://example.org")
	s.refuse(kindGitHub, "someone")
	s.refuse(kindSite, "not a host")
	got := s.refusals()
	if len(got) != 2 || got[0].Value != "example.org" || got[0].Attempts != 2 || got[1].Kind != kindGitHub {
		t.Fatalf("refusals = %+v, want example.org twice and one account", got)
	}
	s.forget(kindSite, "example.org")
	if got := s.refusals(); len(got) != 1 {
		t.Fatalf("after forgetting: %+v, want one left", got)
	}
	for i := range refusedLimit + 50 {
		s.refuse(kindSite, fmt.Sprintf("site-%d.example", i))
	}
	if n := len(s.refusals()); n != refusedLimit {
		t.Fatalf("%d refusals remembered, want at most %d", n, refusedLimit)
	}
}

// TestSourcesAPI allows a site from the dashboard and stops counting it
// again, and checks that counting follows at once.
func TestSourcesAPI(t *testing.T) {
	const site = "sources-test.invalid"
	ctx := context.Background()
	clean := func() {
		if _, err := db.Exec(ctx, `DELETE FROM sources WHERE value LIKE '%sources-test%'`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `DELETE FROM visits WHERE hostname = $1`, site); err != nil {
			t.Fatal(err)
		}
		if err := sources.load(ctx); err != nil {
			t.Fatal(err)
		}
	}
	clean()
	t.Cleanup(clean)

	// The list was imported from allowed.yml when the table was empty.
	if !sources.allowsOrigin("https://changkun.de") || !sources.allowsGitHub("changkun") {
		t.Fatalf("the sources from allowed.yml are not allowed")
	}

	call := func(method string, body any) (int, sourcesResponse) {
		t.Helper()
		b, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		sourcesAPI(w, httptest.NewRequest(method, "/urlstat/dashboard/sources", bytes.NewReader(b)))
		var resp sourcesResponse
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("cannot read the response: %v", err)
			}
		}
		return w.Code, resp
	}
	has := func(list []sourceItem, value string) bool {
		for _, s := range list {
			if s.Value == value {
				return true
			}
		}
		return false
	}
	// A visit as client.js reports it.
	visit := func() int {
		r := httptest.NewRequest(http.MethodGet, "/urlstat?report=page", nil)
		r.Header.Set("urlstat-url", "https://"+site+"/page")
		r.Header.Set("urlstat-ua", "test")
		r.Header.Set("Origin", "https://"+site)
		w := httptest.NewRecorder()
		recording(w, r)
		return w.Code
	}

	// Not on the list: refused, and remembered as refused.
	if got := visit(); got != http.StatusBadRequest {
		t.Fatalf("a visit from a site not on the list = %d, want %d", got, http.StatusBadRequest)
	}
	code, resp := call(http.MethodGet, nil)
	if code != http.StatusOK || !has(resp.Sites, "changkun.de") || has(resp.Sites, site) {
		t.Fatalf("list = %d, changkun.de listed %v, test site listed %v", code, has(resp.Sites, "changkun.de"), has(resp.Sites, site))
	}
	refused := false
	for _, r := range resp.Refused {
		refused = refused || (r.Kind == kindSite && r.Value == site && r.Attempts >= 1)
	}
	if !refused {
		t.Fatalf("the refused site is not among %+v", resp.Refused)
	}

	// Requests that change nothing.
	for name, tt := range map[string]struct {
		method string
		body   map[string]string
		want   int
	}{
		"not a site":   {http.MethodPost, map[string]string{"kind": kindSite, "value": "not a site"}, http.StatusBadRequest},
		"unknown kind": {http.MethodPost, map[string]string{"kind": "other", "value": site}, http.StatusBadRequest},
		"a PUT":        {http.MethodPut, map[string]string{"kind": kindSite, "value": site}, http.StatusMethodNotAllowed},
	} {
		if got, _ := call(tt.method, tt.body); got != tt.want {
			t.Errorf("%s: status = %d, want %d", name, got, tt.want)
		}
	}

	// Allowed, however it was typed: counted at once, and no longer refused.
	code, resp = call(http.MethodPost, map[string]string{"kind": kindSite, "value": "https://Sources-Test.invalid/some/page"})
	if code != http.StatusOK || !has(resp.Sites, site) {
		t.Fatalf("allowing = %d, listed %v", code, has(resp.Sites, site))
	}
	for _, r := range resp.Refused {
		if r.Value == site {
			t.Errorf("an allowed site is still listed as refused")
		}
	}
	if got := visit(); got != http.StatusOK {
		t.Fatalf("a visit from an allowed site = %d, want %d", got, http.StatusOK)
	}

	// Stopped: refused again, and its visit is kept.
	code, resp = call(http.MethodDelete, map[string]string{"kind": kindSite, "value": site})
	if code != http.StatusOK || has(resp.Sites, site) {
		t.Fatalf("stopping = %d, still listed %v", code, has(resp.Sites, site))
	}
	if got := visit(); got != http.StatusBadRequest {
		t.Fatalf("a visit after stopping = %d, want %d", got, http.StatusBadRequest)
	}
	var kept int64
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM visits WHERE hostname = $1`, site).Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("visits kept = %d (%v), want the 1 recorded while allowed", kept, err)
	}

	// A GitHub account, the same way.
	if code, resp = call(http.MethodPost, map[string]string{"kind": kindGitHub, "value": "@Sources-Test"}); code != http.StatusOK || !has(resp.GitHub, "sources-test") || !sources.allowsGitHub("sources-test") {
		t.Fatalf("allowing an account = %d, listed %v", code, has(resp.GitHub, "sources-test"))
	}
}
