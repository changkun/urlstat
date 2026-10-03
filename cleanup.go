// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// cleanupRequest names the pages of one host whose visits are to be
// deleted, for all time: either the given pages, or every page with fewer
// than Below visits, which is mostly crawlers and mistyped addresses.
// Nothing is deleted unless Confirm is set; without it the answer says what
// would be.
type cleanupRequest struct {
	Hostname string   `json:"hostname"`
	Paths    []string `json:"paths"`
	Below    int64    `json:"below"`
	Prefix   string   `json:"prefix"` // narrows Below to the pages under one path
	Confirm  bool     `json:"confirm"`
}

type cleanupResponse struct {
	Pages    int64      `json:"pages"`
	Visits   int64      `json:"visits"`
	Examples []PathItem `json:"examples"` // the busiest of the pages, PV only
	Deleted  bool       `json:"deleted"`
}

const (
	cleanupMaxPaths = 2000
	cleanupMaxBelow = 1000
)

// The pages a cleanup covers, with their visits over all time.
const (
	cleanupPathsSQL = `
		SELECT path, COUNT(*) AS n FROM visits
		WHERE hostname = $1 AND path = ANY($2)
		GROUP BY path`
	cleanupBelowSQL = `
		SELECT path, COUNT(*) AS n FROM visits
		WHERE hostname = $1 AND (path = $2 OR starts_with(path, $2 || '/'))
		GROUP BY path HAVING COUNT(*) < $3`
)

// cleanupAPI previews or deletes the visits of some pages. It is mounted
// behind admin: the endpoint it replaces deleted on any GET, from anyone.
func cleanupAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "cleanup takes a POST", http.StatusMethodNotAllowed)
		return
	}
	var req cleanupRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("cannot read the request: %v", err), http.StatusBadRequest)
		return
	}
	target, args, err := cleanupTarget(&req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	var resp cleanupResponse
	if req.Confirm {
		// Deleting and counting are one statement, so the answer is what
		// was deleted and not what a moment earlier would have been.
		err = db.QueryRow(ctx, `
			WITH target AS (`+target+`), gone AS (
				DELETE FROM visits v USING target t
				WHERE v.hostname = $1 AND v.path = t.path
				RETURNING v.path
			)
			SELECT COUNT(DISTINCT path), COUNT(*) FROM gone`, args...).Scan(&resp.Pages, &resp.Visits)
		resp.Deleted = err == nil
	} else {
		err = db.QueryRow(ctx, `
			WITH target AS (`+target+`)
			SELECT COUNT(*), COALESCE(SUM(n), 0)::bigint FROM target`, args...).Scan(&resp.Pages, &resp.Visits)
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("cleanup failed: %v", err), http.StatusInternalServerError)
		return
	}

	resp.Examples = []PathItem{}
	if !req.Confirm {
		rows, err := db.Query(ctx, `
			WITH target AS (`+target+`)
			SELECT path, n FROM target ORDER BY n DESC, path LIMIT 8`, args...)
		if err != nil {
			http.Error(w, fmt.Sprintf("cleanup failed: %v", err), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var p PathItem
			if err := rows.Scan(&p.Path, &p.PV); err != nil {
				http.Error(w, fmt.Sprintf("cleanup failed: %v", err), http.StatusInternalServerError)
				return
			}
			resp.Examples = append(resp.Examples, p)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, fmt.Sprintf("cleanup failed: %v", err), http.StatusInternalServerError)
			return
		}
	} else {
		log.Printf("cleanup deleted %d visits of %d pages on %s", resp.Visits, resp.Pages, req.Hostname)
		// The dashboard's kept answers still count what is gone.
		dashboardCache.Lock()
		clear(dashboardCache.pages)
		clear(dashboardCache.hosts)
		dashboardCache.Unlock()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// cleanupTarget returns the query for the pages req covers and its
// arguments, or what is wrong with req.
func cleanupTarget(req *cleanupRequest) (string, []any, error) {
	if req.Hostname == "" {
		return "", nil, fmt.Errorf("a cleanup needs a hostname")
	}
	switch {
	case len(req.Paths) > 0 && req.Below != 0:
		return "", nil, fmt.Errorf("a cleanup names pages or a threshold, not both")
	case len(req.Paths) > cleanupMaxPaths:
		return "", nil, fmt.Errorf("a cleanup takes at most %d pages at a time", cleanupMaxPaths)
	case len(req.Paths) > 0:
		return cleanupPathsSQL, []any{req.Hostname, req.Paths}, nil
	case req.Below < 2 || req.Below > cleanupMaxBelow:
		return "", nil, fmt.Errorf("the threshold must be between 2 and %d visits", cleanupMaxBelow)
	}
	prefix := strings.TrimRight(req.Prefix, "/")
	if prefix != "" && !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return cleanupBelowSQL, []any{req.Hostname, prefix, req.Below}, nil
}
