package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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

const dashboardSQL = `
	SELECT path, COUNT(*) as pv, COUNT(DISTINCT ip) as uv
	FROM visits
	WHERE hostname = $1 AND created_at >= $2
	GROUP BY path
	ORDER BY pv DESC, uv DESC`

const hostnamesSQL = `SELECT DISTINCT hostname FROM visits`

const timeseriesSQL = `
	SELECT DATE(created_at) as date, COUNT(*) as pv, COUNT(DISTINCT ip) as uv
	FROM visits
	WHERE hostname = $1 AND created_at >= $2
	GROUP BY DATE(created_at)
	ORDER BY date ASC`

// DashboardAPIResponse is the JSON response for the dashboard API.
type DashboardAPIResponse struct {
	Hostname   string           `json:"hostname"`
	Days       int              `json:"days"`
	Hostnames  []string         `json:"hostnames"`
	Summary    DashboardSummary `json:"summary"`
	Timeseries []TimeseriesItem `json:"timeseries"`
	Paths      []PathItem       `json:"paths"`
}

type DashboardSummary struct {
	TotalPV int64 `json:"total_pv"`
	TotalUV int64 `json:"total_uv"`
}

type TimeseriesItem struct {
	Date string `json:"date"`
	PV   int64  `json:"pv"`
	UV   int64  `json:"uv"`
}

type PathItem struct {
	Path string `json:"path"`
	PV   int64  `json:"pv"`
	UV   int64  `json:"uv"`
}

// dashboardAPI returns JSON data for the dashboard.
func dashboardAPI(w http.ResponseWriter, r *http.Request) {
	// Parse days parameter (default: 30 days)
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if _, err := fmt.Sscanf(d, "%d", &days); err != nil {
			days = 30
		}
	}

	// Validate days range
	if days <= 0 || days > 365 {
		days = 30
	}

	since := time.Now().UTC().AddDate(0, 0, -days)

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	// Get all hostnames
	rows, err := db.Query(ctx, hostnamesSQL)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list hostnames: %v", err), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var hostnames []string
	for rows.Next() {
		var hostname string
		if err := rows.Scan(&hostname); err != nil {
			http.Error(w, fmt.Sprintf("failed to scan hostname: %v", err), http.StatusInternalServerError)
			return
		}
		hostnames = append(hostnames, hostname)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, fmt.Sprintf("failed to iterate hostnames: %v", err), http.StatusInternalServerError)
		return
	}

	// Get hostname parameter (default: first hostname)
	hostname := r.URL.Query().Get("hostname")
	if hostname == "" && len(hostnames) > 0 {
		hostname = hostnames[0]
	}

	response := DashboardAPIResponse{
		Hostname:   hostname,
		Days:       days,
		Hostnames:  hostnames,
		Timeseries: []TimeseriesItem{},
		Paths:      []PathItem{},
	}

	if hostname == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		return
	}

	// Get timeseries data
	start := time.Now()
	log.Printf("querying timeseries for host %v (since %v)", hostname, since)

	tsRows, err := db.Query(ctx, timeseriesSQL, hostname, since)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to query timeseries: %v", err), http.StatusInternalServerError)
		return
	}
	defer tsRows.Close()

	for tsRows.Next() {
		var item TimeseriesItem
		var date time.Time
		if err := tsRows.Scan(&date, &item.PV, &item.UV); err != nil {
			http.Error(w, fmt.Sprintf("failed to scan timeseries: %v", err), http.StatusInternalServerError)
			return
		}
		item.Date = date.Format("2006-01-02")
		response.Timeseries = append(response.Timeseries, item)
	}
	if err := tsRows.Err(); err != nil {
		http.Error(w, fmt.Sprintf("failed to iterate timeseries: %v", err), http.StatusInternalServerError)
		return
	}
	log.Printf("timeseries query for host %v took %v, got %d results", hostname, time.Since(start), len(response.Timeseries))

	// Get path breakdown
	start = time.Now()
	log.Printf("querying paths for host %v (since %v)", hostname, since)

	pathRows, err := db.Query(ctx, dashboardSQL, hostname, since)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to query paths: %v", err), http.StatusInternalServerError)
		return
	}
	defer pathRows.Close()

	var totalPV, totalUV int64
	for pathRows.Next() {
		var item PathItem
		if err := pathRows.Scan(&item.Path, &item.PV, &item.UV); err != nil {
			http.Error(w, fmt.Sprintf("failed to scan path: %v", err), http.StatusInternalServerError)
			return
		}
		response.Paths = append(response.Paths, item)
		totalPV += item.PV
		totalUV += item.UV
	}
	if err := pathRows.Err(); err != nil {
		http.Error(w, fmt.Sprintf("failed to iterate paths: %v", err), http.StatusInternalServerError)
		return
	}
	log.Printf("paths query for host %v took %v, got %d results", hostname, time.Since(start), len(response.Paths))

	response.Summary = DashboardSummary{
		TotalPV: totalPV,
		TotalUV: totalUV,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
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
