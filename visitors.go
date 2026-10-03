// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"time"
)

// Visitors are shown one by one only to whoever is signed in and allowed:
// an address with what it read is personal, where the rest of the dashboard
// is counts. A visitor is an address. Nothing else identifies one, so two
// people behind one address are one visitor, and one person on two networks
// is two.

// visitorsSQL lists the addresses of a period, the latest first, each with
// its latest user agent, where it first came from in the period, and
// whether it was seen before the period.
const visitorsSQL = `
	WITH t AS (
		SELECT ip, COUNT(*) AS pv, COUNT(DISTINCT path) AS pages, MIN(created_at) AS first, MAX(created_at) AS last
		FROM visits WHERE ` + scopeSQL + `
		GROUP BY ip ORDER BY MAX(created_at) DESC LIMIT 200
	)
	SELECT host(t.ip), t.pv, t.pages, t.first, t.last, COALESCE(l.ua, ''), e.came_from,
		EXISTS (SELECT 1 FROM visits b WHERE b.hostname = $1 AND b.ip = t.ip AND b.created_at < $2)
	FROM t
	LEFT JOIN LATERAL (
		SELECT ua FROM visits v WHERE v.hostname = $1 AND v.ip = t.ip AND v.created_at = t.last LIMIT 1
	) l ON true
	LEFT JOIN LATERAL (
		SELECT came_from FROM visits v
		WHERE v.hostname = $1 AND v.ip = t.ip AND v.created_at >= $2 AND v.created_at < $3
			AND v.came_from IS NOT NULL AND v.came_from <> 'internal'
		ORDER BY v.created_at LIMIT 1
	) e ON true
	ORDER BY t.last DESC`

// VisitorItem is one address in a period.
type VisitorItem struct {
	IP       string    `json:"ip"`
	PV       int64     `json:"pv"`
	Pages    int64     `json:"pages"`
	First    time.Time `json:"first"`
	Last     time.Time `json:"last"`
	Browser  string    `json:"browser"`
	OS       string    `json:"os"`
	Device   string    `json:"device"`
	CameFrom *string   `json:"came_from"` // where it first came from in the period; nil when not recorded
	Before   bool      `json:"before"`    // seen before the period began
}

// visitorsAPI lists the latest visitors of a period. It is mounted behind
// admin.
func visitorsAPI(w http.ResponseWriter, r *http.Request) {
	hostname, p, prefix, err := scopeOf(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	rows, err := db.Query(ctx, visitorsSQL, hostname, p.since, p.until, prefix)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list the visitors: %v", err), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	list := []VisitorItem{}
	for rows.Next() {
		var v VisitorItem
		var ua string
		if err := rows.Scan(&v.IP, &v.PV, &v.Pages, &v.First, &v.Last, &ua, &v.CameFrom, &v.Before); err != nil {
			http.Error(w, fmt.Sprintf("failed to list the visitors: %v", err), http.StatusInternalServerError)
			return
		}
		c := classifyUA(ua)
		v.Browser, v.OS, v.Device = c.Browser, c.OS, c.Device
		list = append(list, v)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, fmt.Sprintf("failed to list the visitors: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{"visitors": list})
}

// VisitorDetail is everything one address did on a host: its totals over
// all time and its latest visits.
type VisitorDetail struct {
	IP     string      `json:"ip"`
	PV     int64       `json:"pv"`
	Pages  int64       `json:"pages"`
	Days   int64       `json:"days"`
	First  *time.Time  `json:"first"`
	Last   *time.Time  `json:"last"`
	Agents []string    `json:"agents"` // the user agent strings it used, the latest first
	Visits []VisitItem `json:"visits"` // the latest, at most visitorVisits
}

// VisitItem is one page view.
type VisitItem struct {
	Time     time.Time `json:"time"`
	Path     string    `json:"path"`
	CameFrom *string   `json:"came_from"`
	Referer  string    `json:"referer"`
	Browser  string    `json:"browser"`
	OS       string    `json:"os"`
	Device   string    `json:"device"`
}

const visitorVisits = 300

// visitorAPI returns what one address did on a host. It is mounted behind
// admin.
func visitorAPI(w http.ResponseWriter, r *http.Request) {
	hostname := r.URL.Query().Get("hostname")
	ip, err := netip.ParseAddr(r.URL.Query().Get("ip"))
	if hostname == "" || err != nil {
		http.Error(w, "a hostname and an address are needed", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	d := VisitorDetail{IP: ip.String(), Agents: []string{}, Visits: []VisitItem{}}
	if err := db.QueryRow(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT path), COUNT(DISTINCT (created_at AT TIME ZONE 'UTC')::date), MIN(created_at), MAX(created_at)
		FROM visits WHERE hostname = $1 AND ip = $2`, hostname, ip).Scan(&d.PV, &d.Pages, &d.Days, &d.First, &d.Last); err != nil {
		http.Error(w, fmt.Sprintf("failed to read the visitor: %v", err), http.StatusInternalServerError)
		return
	}
	rows, err := db.Query(ctx, `
		SELECT created_at, path, came_from, COALESCE(referer, ''), COALESCE(ua, '')
		FROM visits WHERE hostname = $1 AND ip = $2
		ORDER BY created_at DESC LIMIT $3`, hostname, ip, visitorVisits)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read the visitor: %v", err), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var v VisitItem
		var ua string
		if err := rows.Scan(&v.Time, &v.Path, &v.CameFrom, &v.Referer, &ua); err != nil {
			http.Error(w, fmt.Sprintf("failed to read the visitor: %v", err), http.StatusInternalServerError)
			return
		}
		c := classifyUA(ua)
		v.Browser, v.OS, v.Device = c.Browser, c.OS, c.Device
		// A visit's stored referer is its own page unless the script
		// reported where it came from.
		if v.CameFrom == nil {
			v.Referer = ""
		}
		d.Visits = append(d.Visits, v)
		if ua != "" && !seen[ua] && len(d.Agents) < 12 {
			seen[ua] = true
			d.Agents = append(d.Agents, ua)
		}
	}
	if err := rows.Err(); err != nil {
		http.Error(w, fmt.Sprintf("failed to read the visitor: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(d)
}
