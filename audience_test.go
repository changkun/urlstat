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
	"net/url"
	"testing"
	"time"
)

const (
	uaChromeMac = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36"
	uaSafariIOS = "Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Mobile/15E148 Safari/604.1"
	uaGooglebot = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
)

// audienceVisits fills a host of its own with a known audience and returns
// the function that removes it again.
//
// Three people and one crawler: 10.2.0.1 reads on a Mac today and three
// days ago, 10.2.0.2 on a Mac, 10.2.0.3 on a phone and arrives from a
// site, and 10.2.0.9 is Googlebot. 10.2.0.1 was also there before the last
// seven days.
func audienceVisits(t *testing.T, host string) {
	t.Helper()
	ctx := context.Background()
	clean := func() {
		if _, err := db.Exec(ctx, `DELETE FROM visits WHERE hostname = $1`, host); err != nil {
			t.Fatal(err)
		}
	}
	clean()
	t.Cleanup(clean)
	now := time.Now().UTC()
	from := "linkedin.com"
	for _, v := range []struct {
		path, ip, ua string
		from         *string
		at           time.Time
	}{
		{"/a", "10.2.0.1", uaChromeMac, nil, now.Add(-4 * time.Minute)},
		{"/b", "10.2.0.1", uaChromeMac, nil, now.Add(-3 * time.Minute)},
		{"/a", "10.2.0.1", uaChromeMac, nil, now.AddDate(0, 0, -3)},
		{"/a", "10.2.0.1", uaChromeMac, nil, now.AddDate(0, 0, -20)},
		{"/a", "10.2.0.2", uaChromeMac, nil, now.Add(-2 * time.Minute)},
		{"/a", "10.2.0.3", uaSafariIOS, &from, now.Add(-1 * time.Minute)},
		{"/a", "10.2.0.9", uaGooglebot, nil, now.Add(-9 * time.Minute)},
		{"/b", "10.2.0.9", uaGooglebot, nil, now.Add(-8 * time.Minute)},
		{"/c", "10.2.0.9", uaGooglebot, nil, now.Add(-7 * time.Minute)},
	} {
		if _, err := db.Exec(ctx, `INSERT INTO visits (hostname, visitor_id, path, ip, ua, came_from, created_at)
			VALUES ($1, gen_random_uuid(), $2, $3, $4, $5, $6)`, host, v.path, v.ip, v.ua, v.from, v.at); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAudienceAPI(t *testing.T) {
	const host = "audience-test.invalid"
	audienceVisits(t, host)

	q := url.Values{"hostname": {host}, "days": {"7"}}
	w := httptest.NewRecorder()
	audienceAPI(w, httptest.NewRequest(http.MethodGet, "/urlstat/dashboard/audience?"+q.Encode(), nil))
	var got AudienceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); w.Code != http.StatusOK || err != nil {
		t.Fatalf("audience = %d %v %s", w.Code, err, w.Body)
	}
	if want := (NamedCount{"people", 5, 3}); got.People != want {
		t.Errorf("people = %+v, want %+v", got.People, want)
	}
	if want := (NamedCount{"crawlers", 3, 1}); got.Crawlers != want {
		t.Errorf("crawlers = %+v, want %+v", got.Crawlers, want)
	}
	// Four addresses, of which one came on two days.
	if got.Visitors != 4 || got.Returning != 1 {
		t.Errorf("%d of %d addresses came back, want 1 of 4", got.Returning, got.Visitors)
	}
	// What people used, busiest first, without the crawler.
	for name, tt := range map[string]struct {
		got, want []NamedCount
	}{
		"devices":  {got.Devices, []NamedCount{{"desktop", 4, 2}, {"mobile", 1, 1}}},
		"systems":  {got.Systems, []NamedCount{{"macOS", 4, 2}, {"iOS", 1, 1}}},
		"browsers": {got.Browsers, []NamedCount{{"Chrome", 4, 2}, {"Safari", 1, 1}}},
	} {
		if len(tt.got) != len(tt.want) {
			t.Errorf("%s = %+v, want %+v", name, tt.got, tt.want)
			continue
		}
		for i := range tt.want {
			if tt.got[i] != tt.want[i] {
				t.Errorf("%s = %+v, want %+v", name, tt.got, tt.want)
				break
			}
		}
	}

	if w := httptest.NewRecorder(); true {
		audienceAPI(w, httptest.NewRequest(http.MethodGet, "/urlstat/dashboard/audience", nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("without a hostname = %d, want %d", w.Code, http.StatusBadRequest)
		}
	}
}

func TestVisitorsAPI(t *testing.T) {
	const host = "visitors-test.invalid"
	audienceVisits(t, host)

	q := url.Values{"hostname": {host}, "days": {"7"}}
	w := httptest.NewRecorder()
	visitorsAPI(w, httptest.NewRequest(http.MethodGet, "/urlstat/dashboard/visitors?"+q.Encode(), nil))
	var list struct {
		Visitors []VisitorItem `json:"visitors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); w.Code != http.StatusOK || err != nil {
		t.Fatalf("visitors = %d %v %s", w.Code, err, w.Body)
	}
	// The latest first.
	var order []string
	by := map[string]VisitorItem{}
	for _, v := range list.Visitors {
		order = append(order, v.IP)
		by[v.IP] = v
	}
	if want := []string{"10.2.0.3", "10.2.0.2", "10.2.0.1", "10.2.0.9"}; len(order) != 4 || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] || order[3] != want[3] {
		t.Fatalf("order = %v, want %v", order, want)
	}
	if v := by["10.2.0.1"]; v.PV != 3 || v.Pages != 2 || !v.Before || v.Device != "desktop" || v.Browser != "Chrome" || v.OS != "macOS" || v.CameFrom != nil {
		t.Errorf("10.2.0.1 = %+v, want 3 views of 2 pages on a Mac, seen before", v)
	}
	if v := by["10.2.0.3"]; v.Device != "mobile" || v.Before || v.CameFrom == nil || *v.CameFrom != "linkedin.com" {
		t.Errorf("10.2.0.3 = %+v, want a phone, new, from linkedin.com", v)
	}
	if v := by["10.2.0.9"]; v.Device != "bot" || v.PV != 3 {
		t.Errorf("10.2.0.9 = %+v, want a crawler with 3 views", v)
	}

	// One visitor, over all time.
	detail := func(ip string) (int, VisitorDetail) {
		q := url.Values{"hostname": {host}, "ip": {ip}}
		w := httptest.NewRecorder()
		visitorAPI(w, httptest.NewRequest(http.MethodGet, "/urlstat/dashboard/visitor?"+q.Encode(), nil))
		var d VisitorDetail
		json.Unmarshal(w.Body.Bytes(), &d)
		return w.Code, d
	}
	code, d := detail("10.2.0.1")
	if code != http.StatusOK || d.PV != 4 || d.Pages != 2 || d.Days != 3 || len(d.Visits) != 4 || len(d.Agents) != 1 || d.Visits[0].Path != "/b" {
		t.Errorf("detail = %d %+v, want 4 views of 2 pages on 3 days, the latest /b", code, d)
	}
	if code, _ := detail("not an address"); code != http.StatusBadRequest {
		t.Errorf("a bad address = %d, want %d", code, http.StatusBadRequest)
	}

	// Cleaning up by address removes that visitor and nobody else.
	clean := func(confirm bool) cleanupResponse {
		b, _ := json.Marshal(cleanupRequest{Hostname: host, IP: "10.2.0.9", Confirm: confirm})
		w := httptest.NewRecorder()
		cleanupAPI(w, httptest.NewRequest(http.MethodPost, "/urlstat/dashboard/cleanup", bytes.NewReader(b)))
		var resp cleanupResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); w.Code != http.StatusOK || err != nil {
			t.Fatalf("cleanup by address = %d %v %s", w.Code, err, w.Body)
		}
		return resp
	}
	if pre := clean(false); pre.Visits != 3 || pre.Pages != 3 || pre.Deleted {
		t.Errorf("preview = %+v, want 3 visits of 3 pages", pre)
	}
	if done := clean(true); done.Visits != 3 || !done.Deleted {
		t.Errorf("cleanup = %+v, want 3 visits deleted", done)
	}
	var left int64
	if err := db.QueryRow(context.Background(), `SELECT COUNT(*) FROM visits WHERE hostname = $1`, host).Scan(&left); err != nil || left != 6 {
		t.Errorf("%d visits left (%v), want the 6 of the three people", left, err)
	}
	b, _ := json.Marshal(cleanupRequest{Hostname: host, IP: "10.2.0.1", Below: 5, Confirm: true})
	w = httptest.NewRecorder()
	cleanupAPI(w, httptest.NewRequest(http.MethodPost, "/urlstat/dashboard/cleanup", bytes.NewReader(b)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("an address and a threshold = %d, want %d", w.Code, http.StatusBadRequest)
	}
}
