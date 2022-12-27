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
			// mongodb query:
			//
			// db.getCollection('golang.design').aggregate([
			// {"$group": {
			//     _id: {path: "$path", ip:"$ip"},
			//     count: {"$sum": 1}}
			// },
			// {"$group": {
			//     _id: "$_id.path",
			//     uv: {$sum: 1},
			//     pv: {$sum: "$count"}}
			// },
			// {"$sort": {'pv': -1, 'uv': -1}}], { allowDiskUse: true })
			//
			// TODO: currently golang.design is the slowest query and should
			// be further optimized. Maybe batched queries?
			p := mongo.Pipeline{
				bson.D{
					primitive.E{
						Key: "$group", Value: bson.M{
							"_id":   bson.M{"path": "$path", "ip": "$ip"},
							"count": bson.M{"$sum": 1},
						},
					},
				},
				bson.D{
					primitive.E{
						Key: "$group", Value: bson.M{
							"_id": "$_id.path",
							"uv":  bson.M{"$sum": 1},
							"pv":  bson.M{"$sum": "$count"},
						},
					},
				},
				bson.D{
					primitive.E{Key: "$sort", Value: bson.M{"pv": -1, "uv": -1}},
				},
			}
			opts := options.Aggregate().SetMaxTime(wait).SetAllowDiskUse(true)
			var cur *mongo.Cursor
			cur, err = col.Aggregate(ctx, p, opts)
			if err != nil {
				err = fmt.Errorf("failed to count visit: %w", err)
				return err
			}
			var results []record
			err = cur.All(ctx, &results)
			if err != nil {
				err = fmt.Errorf("failed to count visit: %w", err)
				return err
			}

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
