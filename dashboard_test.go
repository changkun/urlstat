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

// TestDashboardAPI checks the dashboard's totals, sections and prefix
// against a host of its own. It uses the database the package connects to
// at start, see AGENTS.md.
func TestDashboardAPI(t *testing.T) {
	const host = "dashboard-test.invalid"
	ctx := context.Background()
	clean := func() {
		if _, err := db.Exec(ctx, `DELETE FROM visits WHERE hostname = $1`, host); err != nil {
			t.Fatalf("failed to remove the test visits: %v", err)
		}
	}
	clean()
	t.Cleanup(clean)

	now := time.Now().UTC()
	visits := []struct {
		path, ip string
		at       time.Time
	}{
		{"/", "10.0.0.1", now},
		{"/a/x", "10.0.0.1", now},
		{"/a/x", "10.0.0.2", now},
		{"/a/y/", "10.0.0.2", now},
		{"/a", "10.0.0.3", now},
		{"/ab/z", "10.0.0.4", now},
		// The period before the last 7 days.
		{"/a/x", "10.0.0.9", now.AddDate(0, 0, -8)},
		{"/", "10.0.0.9", now.AddDate(0, 0, -9)},
	}
	for _, v := range visits {
		if _, err := db.Exec(ctx, `INSERT INTO visits (hostname, visitor_id, path, ip, created_at)
			VALUES ($1, gen_random_uuid(), $2, $3, $4)`, host, v.path, v.ip, v.at); err != nil {
			t.Fatalf("failed to insert a visit: %v", err)
		}
	}

	get := func(prefix string) DashboardAPIResponse {
		t.Helper()
		q := url.Values{"hostname": {host}, "days": {"7"}, "prefix": {prefix}}
		w := httptest.NewRecorder()
		dashboardAPI(w, httptest.NewRequest(http.MethodGet, "/urlstat/dashboard/api?"+q.Encode(), nil))
		if w.Code != http.StatusOK {
			t.Fatalf("prefix %q: status %d: %s", prefix, w.Code, w.Body)
		}
		var resp DashboardAPIResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("prefix %q: %v", prefix, err)
		}
		return resp
	}
	sections := func(resp DashboardAPIResponse) map[string]SectionItem {
		m := map[string]SectionItem{}
		for _, s := range resp.Sections {
			m[s.Section] = s
		}
		return m
	}

	// The whole host: visitors are distinct addresses over the period, not
	// a sum over pages, and the previous period is counted on its own.
	all := get("")
	if want := (DashboardSummary{TotalPV: 6, TotalUV: 4, Pages: 5, PrevPV: 2, PrevUV: 1}); all.Summary != want {
		t.Errorf("summary = %+v, want %+v", all.Summary, want)
	}
	if len(all.Timeseries) != 1 || all.Timeseries[0].PV != 6 || all.Timeseries[0].UV != 4 {
		t.Errorf("timeseries = %+v, want one day of 6 views by 4 visitors", all.Timeseries)
	}
	if len(all.Paths) != 5 || all.Paths[0].Path != "/a/x" || all.Paths[0].PV != 2 {
		t.Errorf("paths = %+v, want 5 with /a/x first", all.Paths)
	}
	got := sections(all)
	for name, want := range map[string]SectionItem{
		"":   {Section: "", PV: 1, UV: 1, Pages: 1},
		"a":  {Section: "a", PV: 4, UV: 3, Pages: 3},
		"ab": {Section: "ab", PV: 1, UV: 1, Pages: 1},
	} {
		if got[name] != want {
			t.Errorf("section %q = %+v, want %+v", name, got[name], want)
		}
	}

	// A prefix takes whole path segments: /a holds /a and what is below
	// it, and not /ab. A trailing slash means the same prefix.
	for _, prefix := range []string{"/a", "/a/", "a"} {
		a := get(prefix)
		if a.Prefix != "/a" {
			t.Errorf("prefix %q normalized to %q, want /a", prefix, a.Prefix)
		}
		if want := (DashboardSummary{TotalPV: 4, TotalUV: 3, Pages: 3, PrevPV: 1, PrevUV: 1}); a.Summary != want {
			t.Errorf("prefix %q: summary = %+v, want %+v", prefix, a.Summary, want)
		}
		got := sections(a)
		for name, want := range map[string]SectionItem{
			"":  {Section: "", PV: 1, UV: 1, Pages: 1},
			"x": {Section: "x", PV: 2, UV: 2, Pages: 1},
			"y": {Section: "y", PV: 1, UV: 1, Pages: 1},
		} {
			if got[name] != want {
				t.Errorf("prefix %q: section %q = %+v, want %+v", prefix, name, got[name], want)
			}
		}
	}

	// A chosen range of days, both included, with the range before it as
	// the previous period.
	day := now.AddDate(0, 0, -8).Format(time.DateOnly)
	q := url.Values{"hostname": {host}, "from": {day}, "to": {day}}
	w := httptest.NewRecorder()
	dashboardAPI(w, httptest.NewRequest(http.MethodGet, "/urlstat/dashboard/api?"+q.Encode(), nil))
	var ranged DashboardAPIResponse
	if err := json.Unmarshal(w.Body.Bytes(), &ranged); err != nil {
		t.Fatalf("range: %v", err)
	}
	if !ranged.Custom || ranged.From != day || ranged.To != day || ranged.Days != 1 {
		t.Errorf("range = %s..%s, %d days, custom %v; want one day %s", ranged.From, ranged.To, ranged.Days, ranged.Custom, day)
	}
	if want := (DashboardSummary{TotalPV: 1, TotalUV: 1, Pages: 1, PrevPV: 1, PrevUV: 1}); ranged.Summary != want {
		t.Errorf("range summary = %+v, want %+v", ranged.Summary, want)
	}

	// The host list carries the period's views.
	found := false
	for _, h := range all.Hosts {
		if h.Hostname == host {
			found = h.PV == 6
		}
	}
	if !found {
		t.Errorf("hosts do not list %s with 6 views: %+v", host, all.Hosts)
	}
}

// TestParsePeriod pins how a request's period is read.
func TestParsePeriod(t *testing.T) {
	now := time.Date(2026, 10, 3, 15, 30, 0, 0, time.UTC)
	date := func(s string) time.Time {
		d, err := time.Parse(time.DateOnly, s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	for name, tt := range map[string]struct {
		query string
		want  period
	}{
		"default":            {"", period{since: date("2026-09-04"), until: now, prev: date("2026-08-05"), days: 30}},
		"a week":             {"days=7", period{since: date("2026-09-27"), until: now, prev: date("2026-09-20"), days: 7}},
		"too many days":      {"days=4000", period{since: date("2026-09-04"), until: now, prev: date("2026-08-05"), days: 30}},
		"a range":            {"from=2026-06-01&to=2026-06-14", period{since: date("2026-06-01"), until: date("2026-06-15"), prev: date("2026-05-18"), days: 14, custom: true}},
		"a single day":       {"from=2026-06-08&to=2026-06-08", period{since: date("2026-06-08"), until: date("2026-06-09"), prev: date("2026-06-07"), days: 1, custom: true}},
		"a range to today":   {"from=2026-10-01&to=2026-10-03", period{since: date("2026-10-01"), until: now, prev: date("2026-09-28"), days: 3, custom: true}},
		"past today":         {"from=2026-10-01&to=2027-01-01", period{since: date("2026-10-01"), until: now, prev: date("2026-09-28"), days: 3, custom: true}},
		"backwards":          {"from=2026-06-14&to=2026-06-01", period{since: date("2026-09-04"), until: now, prev: date("2026-08-05"), days: 30}},
		"not dates":          {"from=yesterday&to=today&days=7", period{since: date("2026-09-27"), until: now, prev: date("2026-09-20"), days: 7}},
		"longer than a year": {"from=2020-01-01&to=2026-06-30", period{since: date("2025-06-30"), until: date("2026-07-01"), prev: date("2024-06-29"), days: 366, custom: true}},
	} {
		q, err := url.ParseQuery(tt.query)
		if err != nil {
			t.Fatal(err)
		}
		if got := parsePeriod(q, now); got != tt.want {
			t.Errorf("%s: parsePeriod(%q) =\n\t%+v, want\n\t%+v", name, tt.query, got, tt.want)
		}
	}
}
