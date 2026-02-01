// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

type stat struct {
	PagePV int64 `json:"page_pv"`
	PageUV int64 `json:"page_uv"`
	SitePV int64 `json:"site_pv"`
	SiteUV int64 `json:"site_uv"`
}

type visit struct {
	Hostname  string    `db:"hostname"`
	VisitorID string    `db:"visitor_id"`
	Path      string    `db:"path"`
	IP        string    `db:"ip"`
	UA        string    `db:"ua"`
	Referer   string    `db:"referer"`
	Time      time.Time `db:"created_at"`
}

const urlstatCookieVid = "urlstat_vid"

// recording implmenets a very basic pv/uv statistic function. client script
// is distributed from /urlstat/client.js endpoint.
func recording(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		if source.isAllowed(origin, true) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "urlstat-ua, urlstat-url")
		}
	}
	if r.Method == "OPTIONS" {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Cache-Control", "max-age=0")

	var err error
	defer func() {
		if err == nil {
			return
		}
		http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
	}()

	keys, ok := r.URL.Query()["mode"]
	if ok && len(keys[0]) > 0 && keys[0] == "github" {
		err = githubMode(w, r)
		return
	}

	loc := r.Header.Get("urlstat-url")
	u, err := url.Parse(loc)
	if err != nil {
		err = fmt.Errorf("cannot parse url: %w", err)
		return
	}

	// Double check origin, only allow expected
	ori := fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	if !source.isAllowed(ori, true) {
		err = errors.New("origin not allowed")
		return
	}

	// Save reported statistics to database
	var cookieVid string
	c, err := r.Cookie(urlstatCookieVid)
	if err != nil {
		cookieVid = ""
	} else {
		cookieVid = c.Value
	}

	var vid string
	hostname := u.Host
	vid, err = saveVisit(r.Context(), hostname, &visit{
		VisitorID: cookieVid,
		Path:      u.Path,
		IP:        readIP(r),
		UA:        r.Header.Get("urlstat-ua"),
		Referer:   r.Referer(),
		Time:      time.Now().UTC(),
	})
	if err != nil {
		err = fmt.Errorf("failed to save visit: %w", err)
		return
	}
	if cookieVid == "" && vid != "" {
		w.Header().Set("Set-Cookie", urlstatCookieVid+"="+vid)
	}

	// Report page statistics
	var stat stat
	for _, value := range r.URL.Query()["report"] {
		for arg := range strings.SplitSeq(value, " ") {
			var pv, uv int64
			pv, uv, err = countVisit(r.Context(), hostname, u.Path, arg)
			if err != nil {
				err = fmt.Errorf("failed to count user view count: %w", err)
				return
			}
			switch arg {
			case "page":
				stat.PagePV = pv
				stat.PageUV = uv
			case "site":
				stat.SitePV = pv
				stat.SiteUV = uv
			}
		}
	}

	b, _ := json.Marshal(stat)
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

const insertSQL = `
	INSERT INTO visits (hostname, visitor_id, path, ip, ua, referer, created_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7)`

// saveVisit saves a visit to storage.
func saveVisit(ctx context.Context, hostname string, v *visit) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()

	// if visitor ID does not present, then generate a new visitor ID.
	if v.VisitorID == "" {
		v.VisitorID = uuid.New().String()
	}

	_, err := db.Exec(ctx, insertSQL, hostname, v.VisitorID, v.Path, v.IP, v.UA, v.Referer, v.Time)
	if err != nil {
		return "", fmt.Errorf("failed to insert record: %w", err)
	}
	return v.VisitorID, nil
}

const (
	pageStatsSQL = `
		SELECT COUNT(*) as pv, COUNT(DISTINCT ip) as uv
		FROM visits WHERE hostname = $1 AND path = $2`
	siteStatsSQL = `
		SELECT COUNT(*) as pv, COUNT(DISTINCT ip) as uv
		FROM visits WHERE hostname = $1`
)

// countVisit reports the pv and uv of the given hostname and path location.
func countVisit(ctx context.Context, hostname string, path string, mode string) (pv int64, uv int64, err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()

	switch mode {
	case "site":
		err = db.QueryRow(ctx, siteStatsSQL, hostname).Scan(&pv, &uv)
	case "page":
		err = db.QueryRow(ctx, pageStatsSQL, hostname, path).Scan(&pv, &uv)
	}

	if err != nil {
		return 0, 0, fmt.Errorf("failed to query stats: %w", err)
	}
	return pv, uv, nil
}
