// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCleanupAPI checks that a cleanup first says what it would delete,
// deletes exactly that when confirmed, and leaves other hosts alone.
func TestCleanupAPI(t *testing.T) {
	const host, other = "cleanup-test.invalid", "cleanup-other.invalid"
	ctx := context.Background()
	clean := func() {
		if _, err := db.Exec(ctx, `DELETE FROM visits WHERE hostname IN ($1, $2)`, host, other); err != nil {
			t.Fatalf("failed to remove the test visits: %v", err)
		}
	}
	clean()
	t.Cleanup(clean)

	// /a has 3 visits, /a/x 2, /junk 1, /b 1; the other host has /junk too.
	for _, v := range [][2]string{
		{host, "/a"}, {host, "/a"}, {host, "/a"}, {host, "/a/x"}, {host, "/a/x"}, {host, "/junk"}, {host, "/b"}, {other, "/junk"},
	} {
		if _, err := db.Exec(ctx, `INSERT INTO visits (hostname, visitor_id, path, ip)
			VALUES ($1, gen_random_uuid(), $2, '10.0.0.1')`, v[0], v[1]); err != nil {
			t.Fatalf("failed to insert a visit: %v", err)
		}
	}
	count := func(hostname string) (n int64) {
		t.Helper()
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM visits WHERE hostname = $1`, hostname).Scan(&n); err != nil {
			t.Fatalf("failed to count visits: %v", err)
		}
		return n
	}
	post := func(method string, req any) (int, cleanupResponse) {
		t.Helper()
		body, _ := json.Marshal(req)
		w := httptest.NewRecorder()
		cleanupAPI(w, httptest.NewRequest(method, "/urlstat/dashboard/cleanup", bytes.NewReader(body)))
		var resp cleanupResponse
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("cannot read the response: %v", err)
			}
		}
		return w.Code, resp
	}

	// Requests that must not delete anything.
	for name, tt := range map[string]struct {
		method string
		req    cleanupRequest
		want   int
	}{
		"a GET":               {http.MethodGet, cleanupRequest{Hostname: host, Paths: []string{"/junk"}, Confirm: true}, http.StatusMethodNotAllowed},
		"no hostname":         {http.MethodPost, cleanupRequest{Paths: []string{"/junk"}, Confirm: true}, http.StatusBadRequest},
		"nothing named":       {http.MethodPost, cleanupRequest{Hostname: host, Confirm: true}, http.StatusBadRequest},
		"pages and threshold": {http.MethodPost, cleanupRequest{Hostname: host, Paths: []string{"/junk"}, Below: 5, Confirm: true}, http.StatusBadRequest},
		"threshold of one":    {http.MethodPost, cleanupRequest{Hostname: host, Below: 1, Confirm: true}, http.StatusBadRequest},
	} {
		if got, _ := post(tt.method, tt.req); got != tt.want {
			t.Errorf("%s: status = %d, want %d", name, got, tt.want)
		}
	}
	if n := count(host); n != 7 {
		t.Fatalf("refused requests left %d visits, want all 7", n)
	}

	// Named pages: the preview counts, and deletes nothing.
	code, resp := post(http.MethodPost, cleanupRequest{Hostname: host, Paths: []string{"/junk", "/a/x", "/missing"}})
	if code != http.StatusOK || resp.Pages != 2 || resp.Visits != 3 || resp.Deleted {
		t.Fatalf("preview = %d %+v, want 2 pages, 3 visits, not deleted", code, resp)
	}
	if len(resp.Examples) != 2 || resp.Examples[0] != (PathItem{Path: "/a/x", PV: 2}) {
		t.Errorf("examples = %+v, want /a/x with 2 visits first", resp.Examples)
	}
	if n := count(host); n != 7 {
		t.Fatalf("a preview left %d visits, want all 7", n)
	}

	// Confirmed: exactly those are gone, here and nowhere else.
	code, resp = post(http.MethodPost, cleanupRequest{Hostname: host, Paths: []string{"/junk", "/a/x", "/missing"}, Confirm: true})
	if code != http.StatusOK || resp.Pages != 2 || resp.Visits != 3 || !resp.Deleted {
		t.Fatalf("cleanup = %d %+v, want 2 pages, 3 visits, deleted", code, resp)
	}
	if n, m := count(host), count(other); n != 4 || m != 1 {
		t.Fatalf("after the cleanup: %d visits here and %d on the other host, want 4 and 1", n, m)
	}

	// A threshold under a prefix: /a has 3 visits and stays at "fewer than
	// 3", and /b is outside the prefix.
	if _, err := db.Exec(ctx, `INSERT INTO visits (hostname, visitor_id, path, ip) VALUES ($1, gen_random_uuid(), '/a/y', '10.0.0.1')`, host); err != nil {
		t.Fatalf("failed to insert a visit: %v", err)
	}
	code, resp = post(http.MethodPost, cleanupRequest{Hostname: host, Below: 3, Prefix: "/a/", Confirm: true})
	if code != http.StatusOK || resp.Pages != 1 || resp.Visits != 1 {
		t.Fatalf("threshold cleanup = %d %+v, want 1 page, 1 visit", code, resp)
	}
	if n := count(host); n != 4 {
		t.Fatalf("after the threshold cleanup: %d visits, want 4 (/a three times and /b)", n)
	}
}
