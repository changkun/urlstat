package main

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
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

	ctx, cancel := context.WithTimeout(r.Context(), time.Second*10)
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

	var all []records
	for _, hostname := range cols {
		col := db.Database(dbname).Collection(hostname)
		// mongodb query:
		//
		// db.getCollection('blog.changkun.de').aggregate([
		// {"$group": {
		//     _id: {path: "$path", ip:"$ip"},
		//     count: {"$sum": 1}}
		// },
		// {"$group": {
		//     _id: "$_id.path",
		//     uv: {$sum: 1},
		//     pv: {$sum: "$count"}}
		// }])
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
		}
		opts := options.Aggregate().SetMaxTime(10 * time.Second)
		var cur *mongo.Cursor
		cur, err = col.Aggregate(ctx, p, opts)
		if err != nil {
			err = fmt.Errorf("failed to count visit: %w", err)
			return
		}
		var results []record
		err = cur.All(ctx, &results)
		if err != nil {
			err = fmt.Errorf("failed to count visit: %w", err)
			return
		}
		all = append(all, records{
			Host:    hostname,
			Records: results,
		})
	}

	for idx := range all {
		sort.Slice(all[idx].Records, func(i, j int) bool {
			if all[idx].Records[i].PV > all[idx].Records[j].PV {
				return true
			}
			return all[idx].Records[i].UV > all[idx].Records[j].UV
		})
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
