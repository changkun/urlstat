package main

import (
	"context"
	"fmt"
	"html/template"
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

// dashboard returns a simple dashboard view to view all existing statistics.
func dashboard(w http.ResponseWriter, r *http.Request) {
	var err error
	defer func() {
		if err == nil {
			return
		}
		http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
	}()

	// Parse days parameter (default: 30 days, 0 = all time)
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if _, err := fmt.Sscanf(d, "%d", &days); err != nil {
			days = 30
		}
	}

	// Always filter by time - default 30 days, max 365 days for performance
	filterDays := days
	if filterDays <= 0 || filterDays > 365 {
		filterDays = 365 // Cap at 1 year to prevent runaway queries
	}
	since := time.Now().UTC().AddDate(0, 0, -filterDays)

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	// Get all hostnames
	rows, err := db.Query(ctx, hostnamesSQL)
	if err != nil {
		err = fmt.Errorf("failed to list hostnames: %w", err)
		return
	}
	defer rows.Close()

	var hostnames []string
	for rows.Next() {
		var hostname string
		if err = rows.Scan(&hostname); err != nil {
			err = fmt.Errorf("failed to scan hostname: %w", err)
			return
		}
		hostnames = append(hostnames, hostname)
	}
	if err = rows.Err(); err != nil {
		err = fmt.Errorf("failed to iterate hostnames: %w", err)
		return
	}

	type record struct {
		Path string
		PV   int64
		UV   int64
	}
	type records struct {
		Host    string
		Records []record
	}

	all := make([]records, 0, len(hostnames))

	for _, hostname := range hostnames {
		start := time.Now()
		log.Printf("querying stats for host %v (since %v)", hostname, since)

		statsRows, err := db.Query(ctx, dashboardSQL, hostname, since)
		if err != nil {
			log.Printf("failed to query stats for %s: %v", hostname, err)
			continue
		}

		var results []record
		for statsRows.Next() {
			var r record
			if err := statsRows.Scan(&r.Path, &r.PV, &r.UV); err != nil {
				log.Printf("failed to scan stats for %s: %v", hostname, err)
				continue
			}
			results = append(results, r)
		}
		statsRows.Close()

		if statsRows.Err() != nil {
			log.Printf("error iterating stats for %s: %v", hostname, statsRows.Err())
			continue
		}

		all = append(all, records{
			Host:    hostname,
			Records: results,
		})
		log.Printf("running for host %v took %v, got %d results", hostname, time.Since(start), len(results))
	}

	t, err := template.ParseFS(publicFS, "dashboard.html")
	if err != nil {
		err = fmt.Errorf("failed to parse dashboard.html: %w", err)
		return
	}
	err = t.Execute(w, struct{ All []records }{all})
	if err != nil {
		err = fmt.Errorf("failed to render template: %w", err)
	}
}
