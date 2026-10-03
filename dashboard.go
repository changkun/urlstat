package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// handleCleanup is the HTTP handler for the cleanup endpoint.
func handleCleanup(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	deleted, err := cleanup(ctx)
	if err != nil {
		http.Error(w, fmt.Sprintf("cleanup failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "Cleanup complete. Deleted %d low-visit entries (paths with <10 visits).\n", deleted)
}

const cleanupSQL = `
	WITH low_visit_paths AS (
		SELECT hostname, path FROM visits
		GROUP BY hostname, path HAVING COUNT(*) < 10
	)
	DELETE FROM visits v USING low_visit_paths lvp
	WHERE v.hostname = lvp.hostname AND v.path = lvp.path`

// cleanup removes entries with fewer than 10 visits (likely bots or noise).
// It returns the total number of deleted documents.
func cleanup(ctx context.Context) (int64, error) {
	result, err := db.Exec(ctx, cleanupSQL)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup: %w", err)
	}
	return result.RowsAffected(), nil
}

// Every dashboard query reads one host over [since, until), optionally
// narrowed to the pages under a path prefix. The prefix is matched by whole
// segments, so "/modern" does not also take "/modern-cpp"; an empty prefix
// matches every path, since every path starts with a slash.
const scopeSQL = `hostname = $1 AND created_at >= $2 AND created_at < $3
	AND (path = $4 OR starts_with(path, $4 || '/'))`

// hostsSQL lists the hosts with their page views since $1, busiest first.
// The hosts come from a loose index scan, one index probe per host: a plain
// SELECT DISTINCT reads every row of the table and took seconds.
const hostsSQL = `
	WITH RECURSIVE h AS (
		(SELECT hostname FROM visits ORDER BY hostname LIMIT 1)
		UNION ALL
		SELECT (SELECT v.hostname FROM visits v WHERE v.hostname > h.hostname ORDER BY v.hostname LIMIT 1)
		FROM h WHERE h.hostname IS NOT NULL
	)
	SELECT h.hostname,
		(SELECT COUNT(*) FROM visits v WHERE v.hostname = h.hostname AND v.created_at >= $1) AS pv
	FROM h WHERE h.hostname IS NOT NULL
	ORDER BY pv DESC, h.hostname`

// The server this runs on has a single processor, and COUNT(DISTINCT ip)
// sorts every row it is given. So visitors are counted in two steps, which
// hash: first one row per address, then the rows.

// overviewSQL reads the period once and answers three questions from that
// one pass: the totals (kind 0), the pages grouped by their next path
// segment below the prefix (kind 1; $5 is that segment's position in the
// path, and the segment is empty for the prefix's own page), and the pages
// (kind 2).
const overviewSQL = `
	WITH v AS MATERIALIZED (
		SELECT path, ip, COUNT(*) AS n FROM visits WHERE ` + scopeSQL + `
		GROUP BY path, ip
	), p AS (
		SELECT path, SUM(n)::bigint AS pv, COUNT(*) AS uv FROM v GROUP BY path
	), s AS (
		SELECT a.section, a.pv, a.uv, b.pages
		FROM (
			SELECT section, SUM(n)::bigint AS pv, COUNT(*) AS uv
			FROM (SELECT split_part(path, '/', $5) AS section, ip, SUM(n) AS n FROM v GROUP BY 1, 2) t
			GROUP BY section
		) a JOIN (
			SELECT split_part(path, '/', $5) AS section, COUNT(*) AS pages FROM p GROUP BY 1
		) b USING (section)
	)
	SELECT 0 AS kind, '' AS name,
		(SELECT COALESCE(SUM(n), 0)::bigint FROM v) AS pv,
		(SELECT COUNT(*) FROM (SELECT 1 FROM v GROUP BY ip) i) AS uv,
		(SELECT COUNT(*) FROM p) AS pages
	UNION ALL (SELECT 1, section, pv, uv, pages FROM s ORDER BY pv DESC, uv DESC LIMIT 100)
	UNION ALL (SELECT 2, path, pv, uv, 1 FROM p ORDER BY pv DESC, uv DESC LIMIT 1000)`

// previousSQL totals the period before, for the change shown on the totals.
const previousSQL = `
	SELECT COALESCE(SUM(n), 0)::bigint, COUNT(*)
	FROM (SELECT ip, COUNT(*) AS n FROM visits WHERE ` + scopeSQL + ` GROUP BY ip) t`

const timeseriesSQL = `
	SELECT date, SUM(n)::bigint AS pv, COUNT(*) AS uv
	FROM (
		SELECT (created_at AT TIME ZONE 'UTC')::date AS date, ip, COUNT(*) AS n
		FROM visits WHERE ` + scopeSQL + ` GROUP BY 1, 2
	) t
	GROUP BY date ORDER BY date`

// DashboardAPIResponse is the JSON response for the dashboard API.
type DashboardAPIResponse struct {
	Hostname   string           `json:"hostname"`
	Days       int              `json:"days"`
	Prefix     string           `json:"prefix"`
	Hosts      []HostItem       `json:"hosts"`
	Summary    DashboardSummary `json:"summary"`
	Timeseries []TimeseriesItem `json:"timeseries"`
	Sections   []SectionItem    `json:"sections"`
	Paths      []PathItem       `json:"paths"`
}

type HostItem struct {
	Hostname string `json:"hostname"`
	PV       int64  `json:"pv"`
}

// DashboardSummary counts the selected period and the one before it.
// Visitors are distinct addresses over the whole period, not a sum over pages.
type DashboardSummary struct {
	TotalPV int64 `json:"total_pv"`
	TotalUV int64 `json:"total_uv"`
	Pages   int64 `json:"pages"`
	PrevPV  int64 `json:"prev_pv"`
	PrevUV  int64 `json:"prev_uv"`
}

type TimeseriesItem struct {
	Date string `json:"date"`
	PV   int64  `json:"pv"`
	UV   int64  `json:"uv"`
}

type SectionItem struct {
	Section string `json:"section"`
	PV      int64  `json:"pv"`
	UV      int64  `json:"uv"`
	Pages   int64  `json:"pages"`
}

type PathItem struct {
	Path string `json:"path"`
	PV   int64  `json:"pv"`
	UV   int64  `json:"uv"`
}

// The dashboard is public and its queries aggregate up to a year of visits,
// so finished responses are kept for a while: a minute for a month, longer
// for a longer period, whose figures move less.
const (
	hostsTTL   = 5 * time.Minute
	cacheLimit = 256
)

func dashboardTTL(days int) time.Duration {
	return max(time.Minute, time.Duration(days)*2*time.Second)
}

type cached[T any] struct {
	at  time.Time
	val T
}

var dashboardCache = struct {
	sync.Mutex
	pages map[string]cached[[]byte]
	hosts map[int]cached[[]HostItem]
}{pages: map[string]cached[[]byte]{}, hosts: map[int]cached[[]HostItem]{}}

// listHosts returns the hosts and their page views over the given days.
func listHosts(ctx context.Context, days int, since time.Time) ([]HostItem, error) {
	dashboardCache.Lock()
	c, ok := dashboardCache.hosts[days]
	dashboardCache.Unlock()
	if ok && time.Since(c.at) < hostsTTL {
		return c.val, nil
	}

	rows, err := db.Query(ctx, hostsSQL, since)
	if err != nil {
		return nil, fmt.Errorf("failed to list hosts: %w", err)
	}
	defer rows.Close()
	hosts := []HostItem{}
	for rows.Next() {
		var h HostItem
		if err := rows.Scan(&h.Hostname, &h.PV); err != nil {
			return nil, fmt.Errorf("failed to scan host: %w", err)
		}
		hosts = append(hosts, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate hosts: %w", err)
	}

	dashboardCache.Lock()
	dashboardCache.hosts[days] = cached[[]HostItem]{time.Now(), hosts}
	dashboardCache.Unlock()
	return hosts, nil
}

// dashboardAPI returns JSON data for the dashboard: the hosts, and for one
// host over the last days (30 by default, at most 365) its totals, daily
// counts, sections and pages. The prefix parameter narrows all of them to the
// pages under one path, which is how a section such as /bobook is totalled.
func dashboardAPI(w http.ResponseWriter, r *http.Request) {
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if _, err := fmt.Sscanf(d, "%d", &days); err != nil {
			days = 30
		}
	}
	if days <= 0 || days > 365 {
		days = 30
	}
	prefix := strings.TrimRight(r.URL.Query().Get("prefix"), "/")
	if prefix != "" && !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}

	// The period is whole UTC days, today included, so that the first day
	// of the chart is not a partial one.
	until := time.Now().UTC()
	since := until.Truncate(24*time.Hour).AddDate(0, 0, -(days - 1))
	prev := since.AddDate(0, 0, -days)

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	start := time.Now()

	hosts, err := listHosts(ctx, days, since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Without a host, show the busiest one of the period.
	hostname := r.URL.Query().Get("hostname")
	if hostname == "" && len(hosts) > 0 {
		hostname = hosts[0].Hostname
	}

	key := fmt.Sprintf("%s\x00%d\x00%s", hostname, days, prefix)
	dashboardCache.Lock()
	c, ok := dashboardCache.pages[key]
	dashboardCache.Unlock()
	if ok && time.Since(c.at) < dashboardTTL(days) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(c.val)
		return
	}

	resp := DashboardAPIResponse{
		Hostname:   hostname,
		Days:       days,
		Prefix:     prefix,
		Hosts:      hosts,
		Timeseries: []TimeseriesItem{},
		Sections:   []SectionItem{},
		Paths:      []PathItem{},
	}
	if hostname == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	// The queries are independent, so they run side by side where the
	// machine has the processors for it.
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	run := func(name string, f func() error) {
		wg.Go(func() {
			if err := f(); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("failed to query %s: %w", name, err))
				mu.Unlock()
			}
		})
	}
	run("overview", func() error {
		// The grouping hashes a year of rows; with the default work
		// memory it would spill to disk.
		tx, err := db.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "SET LOCAL work_mem = '32MB'"); err != nil {
			return err
		}
		// A path "/a/b" splits on "/" into "", "a", "b": the segment below
		// a prefix of n segments is part n+2.
		rows, err := tx.Query(ctx, overviewSQL, hostname, since, until, prefix, strings.Count(prefix, "/")+2)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				kind          int
				name          string
				pv, uv, pages int64
			)
			if err := rows.Scan(&kind, &name, &pv, &uv, &pages); err != nil {
				return err
			}
			switch kind {
			case 0:
				resp.Summary.TotalPV, resp.Summary.TotalUV, resp.Summary.Pages = pv, uv, pages
			case 1:
				resp.Sections = append(resp.Sections, SectionItem{Section: name, PV: pv, UV: uv, Pages: pages})
			case 2:
				resp.Paths = append(resp.Paths, PathItem{Path: name, PV: pv, UV: uv})
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// Busiest first; the name settles ties so the order is stable.
		slices.SortFunc(resp.Sections, func(a, b SectionItem) int {
			return cmp.Or(cmp.Compare(b.PV, a.PV), cmp.Compare(b.UV, a.UV), cmp.Compare(a.Section, b.Section))
		})
		slices.SortFunc(resp.Paths, func(a, b PathItem) int {
			return cmp.Or(cmp.Compare(b.PV, a.PV), cmp.Compare(b.UV, a.UV), cmp.Compare(a.Path, b.Path))
		})
		return nil
	})
	run("previous period", func() error {
		return db.QueryRow(ctx, previousSQL, hostname, prev, since, prefix).
			Scan(&resp.Summary.PrevPV, &resp.Summary.PrevUV)
	})
	run("timeseries", func() error {
		rows, err := db.Query(ctx, timeseriesSQL, hostname, since, until, prefix)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item TimeseriesItem
			var date time.Time
			if err := rows.Scan(&date, &item.PV, &item.UV); err != nil {
				return err
			}
			item.Date = date.Format("2006-01-02")
			resp.Timeseries = append(resp.Timeseries, item)
		}
		return rows.Err()
	})
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("dashboard for host %v, %d days, prefix %q took %v", hostname, days, prefix, time.Since(start))

	body, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to encode response: %v", err), http.StatusInternalServerError)
		return
	}
	dashboardCache.Lock()
	if len(dashboardCache.pages) >= cacheLimit {
		clear(dashboardCache.pages)
	}
	dashboardCache.pages[key] = cached[[]byte]{time.Now(), body}
	dashboardCache.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

// dashboard serves the static dashboard HTML page.
func dashboard(w http.ResponseWriter, r *http.Request) {
	f, err := publicFS.Open("dashboard.html")
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to open dashboard.html: %v", err), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.Copy(w, f)
}
