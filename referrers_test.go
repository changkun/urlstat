// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// TestReferrers records visits the way client.js reports them and checks
// that the dashboard says where they came from.
func TestReferrers(t *testing.T) {
	const site = "referrers-test.invalid"
	ctx := context.Background()
	clean := func() {
		for _, q := range []string{`DELETE FROM visits WHERE hostname = $1`, `DELETE FROM sources WHERE value = $1`} {
			if _, err := db.Exec(ctx, q, site); err != nil {
				t.Fatal(err)
			}
		}
		if err := sources.load(ctx); err != nil {
			t.Fatal(err)
		}
	}
	clean()
	t.Cleanup(clean)
	if _, err := db.Exec(ctx, `INSERT INTO sources (kind, value) VALUES ('site', $1)`, site); err != nil {
		t.Fatal(err)
	}
	if err := sources.load(ctx); err != nil {
		t.Fatal(err)
	}

	// visit reports one page view from the given address; ref is what the
	// script sends as urlstat-ref, and nil is a script that sends none.
	visit := func(page, ip string, ref *string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/urlstat?report=page", nil)
		r.Header.Set("urlstat-url", "https://"+site+page)
		r.Header.Set("urlstat-ua", "test")
		r.Header.Set("X-Forwarded-For", ip)
		r.Header.Set("Referer", "https://"+site+page) // what a browser's fetch carries
		if ref != nil {
			r.Header.Set("urlstat-ref", *ref)
		}
		w := httptest.NewRecorder()
		recording(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("visit to %s = %d: %s", page, w.Code, w.Body)
		}
	}
	s := func(v string) *string { return &v }
	visit("/book/", "10.1.0.1", s("https://www.linkedin.com/feed/"))
	visit("/book/", "10.1.0.2", s("android-app://com.linkedin.android/"))
	visit("/book/?utm_source=linkedin", "10.1.0.2", s("none"))
	visit("/book/ch1", "10.1.0.1", s("https://"+site+"/book/"))
	visit("/book/", "10.1.0.3", s("none"))
	visit("/blog/", "10.1.0.4", s("https://www.google.de/"))
	visit("/blog/", "10.1.0.5", nil) // an older script: counted as a view, from nowhere known

	// The preflight lets the new header through.
	pre := httptest.NewRequest(http.MethodOptions, "/urlstat", nil)
	pre.Header.Set("Origin", "https://"+site)
	w := httptest.NewRecorder()
	recording(w, pre)
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "urlstat-ua, urlstat-url, urlstat-ref" {
		t.Errorf("allowed headers = %q, want the three the script sends", got)
	}

	get := func(prefix string) DashboardAPIResponse {
		t.Helper()
		q := url.Values{"hostname": {site}, "days": {"7"}, "prefix": {prefix}}
		w := httptest.NewRecorder()
		dashboardAPI(w, httptest.NewRequest(http.MethodGet, "/urlstat/dashboard/api?"+q.Encode(), nil))
		var resp DashboardAPIResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); w.Code != http.StatusOK || err != nil {
			t.Fatalf("prefix %q: %d %v %s", prefix, w.Code, err, w.Body)
		}
		return resp
	}
	from := func(resp DashboardAPIResponse) map[string]ReferrerItem {
		m := map[string]ReferrerItem{}
		for _, r := range resp.Referrers {
			m[r.Source] = r
		}
		return m
	}

	all := get("")
	if all.Summary.TotalPV != 7 {
		t.Fatalf("views = %d, want all 7, the one from an older script included", all.Summary.TotalPV)
	}
	if want := time.Now().UTC().Format(time.DateOnly); all.ReferrersSince != want {
		t.Errorf("recording since %q, want %q", all.ReferrersSince, want)
	}
	got := from(all)
	for source, want := range map[string]ReferrerItem{
		"linkedin.com": {Source: "linkedin.com", PV: 3, UV: 2},
		"google.com":   {Source: "google.com", PV: 1, UV: 1},
		fromDirect:     {Source: fromDirect, PV: 1, UV: 1},
		fromInternal:   {Source: fromInternal, PV: 1, UV: 1},
	} {
		if got[source] != want {
			t.Errorf("from %q = %+v, want %+v", source, got[source], want)
		}
	}
	if len(got) != 4 {
		t.Errorf("referrers = %+v, want four places", all.Referrers)
	}

	// Under a prefix, only that section's visits.
	if got := from(get("/book")); len(got) != 3 || got["linkedin.com"].PV != 3 || got["google.com"].PV != 0 {
		t.Errorf("under /book: %+v, want linkedin.com, direct and internal", got)
	}

	// What was sent is kept as it was, and an older script leaves it unknown.
	var kept, unknown int64
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE referer = 'https://www.linkedin.com/feed/'),
		COUNT(*) FILTER (WHERE came_from IS NULL) FROM visits WHERE hostname = $1`, site).Scan(&kept, &unknown); err != nil || kept != 1 || unknown != 1 {
		t.Errorf("kept the address %d times and left %d unknown (%v), want 1 and 1", kept, unknown, err)
	}
}
