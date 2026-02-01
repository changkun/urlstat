package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"runtime"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"golang.org/x/sync/errgroup"
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

// cleanup removes entries with fewer than 10 visits (likely bots or noise).
// It returns the total number of deleted documents across all collections.
func cleanup(ctx context.Context) (int64, error) {
	cols, err := db.Database(dbname).ListCollectionNames(ctx, bson.D{})
	if err != nil {
		return 0, fmt.Errorf("failed to list collections: %w", err)
	}

	var totalDeleted int64
	for _, hostname := range cols {
		col := db.Database(dbname).Collection(hostname)

		// Find paths with fewer than 10 visits
		p := mongo.Pipeline{
			bson.D{{Key: "$group", Value: bson.M{
				"_id":   "$path",
				"count": bson.M{"$sum": 1},
			}}},
			bson.D{{Key: "$match", Value: bson.M{
				"count": bson.M{"$lt": 10},
			}}},
		}

		cur, err := col.Aggregate(ctx, p)
		if err != nil {
			log.Printf("cleanup: failed to aggregate %s: %v", hostname, err)
			continue
		}

		var results []struct {
			Path  string `bson:"_id"`
			Count int64  `bson:"count"`
		}
		if err := cur.All(ctx, &results); err != nil {
			log.Printf("cleanup: failed to decode results for %s: %v", hostname, err)
			continue
		}

		if len(results) == 0 {
			continue
		}

		// Log and collect paths to delete
		paths := make([]string, len(results))
		for i, r := range results {
			paths[i] = r.Path
			log.Printf("cleanup: removing low-visit path from %s: %s (count: %d)", hostname, r.Path, r.Count)
		}

		// Delete documents with these paths
		res, err := col.DeleteMany(ctx, bson.M{"path": bson.M{"$in": paths}})
		if err != nil {
			log.Printf("cleanup: failed to delete from %s: %v", hostname, err)
			continue
		}

		if res.DeletedCount > 0 {
			log.Printf("cleanup: deleted %d low-visit entries from %s", res.DeletedCount, hostname)
			totalDeleted += res.DeletedCount
		}
	}

	return totalDeleted, nil
}

// dashboard returns a simple dashboard view to view all existing statistics.
func dashboard(w http.ResponseWriter, r *http.Request) {
	var err error
	defer func() {
		if err == nil {
			return
		}
		http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
	}()
	wait := 60 * time.Second

	// Parse days parameter (default: 30 days, 0 = all time)
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if _, err := fmt.Sscanf(d, "%d", &days); err != nil {
			days = 30
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), wait)
	defer cancel()

	cols, err := db.Database(dbname).ListCollectionNames(ctx, bson.D{})
	if err != nil {
		err = fmt.Errorf("failed to list collections: %w", err)
		return
	}
	type record struct {
		Path string `bson:"_id"`
		PV   int64  `bson:"pv"`
		UV   int64  `bson:"uv"`
	}
	type records struct {
		Host    string
		Records []record
	}

	all := make([]records, 0, len(cols))
	mu := sync.Mutex{}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for _, hostname := range cols {
		hostname := hostname
		g.Go(func() error {
			start := time.Now()
			defer func() {
				log.Printf("running for host %v took %v", hostname, time.Since(start))
			}()

			col := db.Database(dbname).Collection(hostname)

			// Debug: log document count and index info
			docCount, _ := col.EstimatedDocumentCount(ctx)
			log.Printf("DEBUG %s: estimated doc count = %d", hostname, docCount)

			// Check indexes
			indexCursor, _ := col.Indexes().List(ctx)
			var indexes []bson.M
			indexCursor.All(ctx, &indexes)
			for _, idx := range indexes {
				log.Printf("DEBUG %s: index = %v", hostname, idx["key"])
			}

			// Build pipeline with optional time filter
			p := mongo.Pipeline{}

			// Add $match stage if filtering by time (days > 0)
			if days > 0 {
				since := time.Now().UTC().AddDate(0, 0, -days)
				p = append(p, bson.D{
					primitive.E{
						Key: "$match", Value: bson.M{
							"time": bson.M{"$gte": since},
						},
					},
				})
			}

			// Group by {path, ip} to count visits per unique visitor per path
			p = append(p, bson.D{
				primitive.E{
					Key: "$group", Value: bson.M{
						"_id":   bson.M{"path": "$path", "ip": "$ip"},
						"count": bson.M{"$sum": 1},
					},
				},
			})

			// Group by path to get UV (unique IPs) and PV (total visits)
			p = append(p, bson.D{
				primitive.E{
					Key: "$group", Value: bson.M{
						"_id": "$_id.path",
						"uv":  bson.M{"$sum": 1},
						"pv":  bson.M{"$sum": "$count"},
					},
				},
			})

			// Sort by PV descending
			p = append(p, bson.D{
				primitive.E{Key: "$sort", Value: bson.M{"pv": -1, "uv": -1}},
			})
			opts := options.Aggregate().SetMaxTime(wait).SetAllowDiskUse(true)

			aggStart := time.Now()
			var cur *mongo.Cursor
			cur, err = col.Aggregate(ctx, p, opts)
			if err != nil {
				err = fmt.Errorf("failed to count visit: %w", err)
				return err
			}
			log.Printf("DEBUG %s: aggregate() took %v", hostname, time.Since(aggStart))

			cursorStart := time.Now()
			var results []record
			err = cur.All(ctx, &results)
			if err != nil {
				err = fmt.Errorf("failed to count visit: %w", err)
				return err
			}
			log.Printf("DEBUG %s: cursor.All() took %v, got %d results", hostname, time.Since(cursorStart), len(results))

			mu.Lock()
			all = append(all, records{
				Host:    hostname,
				Records: results,
			})
			mu.Unlock()
			return nil
		})
	}
	if err = g.Wait(); err != nil {
		return
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
