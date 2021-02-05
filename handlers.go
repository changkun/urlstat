// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

type stat struct {
	PagePV int64 `json:"page_pv"`
	PageUV int64 `json:"page_uv"`
}

type visit struct {
	Path string    `json:"path" bson:"path"`
	IP   string    `json:"ip"   bson:"ip"`
	UA   string    `json:"ua"   bson:"ua"`
	Time time.Time `json:"time" bson:"time"`
}

// TODO: allow more origins, and use regexp for matching.
var allowedOrigin = []string{
	// "http://localhost",
	"https://changkun.de",
	"https://www.changkun.de",
	"https://blog.changkun.de",
	"https://golang.design",
	"https://www.golang.design",
}

// recording implmenets a very basic pv/uv statistic function. client script
// is distributed from /urlstat/client.js endpoint.
func recording(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "urlstat-ua, urlstat-url")
	}
	if r.Method == "OPTIONS" {
		return
	}

	var err error
	defer func() {
		if err == nil {
			return
		}
		http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
	}()

	loc := r.Header.Get("urlstat-url")
	u, err := url.Parse(loc)
	if err != nil {
		err = fmt.Errorf("cannot parse url: %w", err)
		return
	}

	// Double check origin, only allow expected
	allow := false
	ori := fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	for idx := range allowedOrigin {
		if strings.Contains(ori, allowedOrigin[idx]) {
			allow = true
			break
		}
	}
	if !allow {
		err = errors.New("origin not allowed")
		return
	}

	// Save reported statistics to database
	ip := readIP(r)
	v := visit{
		Path: u.Path,
		IP:   ip,
		UA:   r.Header.Get("urlstat-ua"),
		Time: time.Now().UTC(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	col := db.Database(dbname).Collection(u.Host)
	_, err = col.InsertOne(ctx, v)
	if err != nil {
		err = fmt.Errorf("failed to insert record: %w", err)
		return
	}

	// Report existing statistics
	pv, uv, err := countVisit(ctx, col, u.Path)
	if err != nil {
		err = fmt.Errorf("failed to count user view count: %w", err)
		return
	}
	b, _ := json.Marshal(stat{PagePV: pv, PageUV: uv})
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
	return
}

// dashboard returns a simple dashboard view to view all existing statistics.
func dashbaord(w http.ResponseWriter, r *http.Request) {
	var err error
	defer func() {
		if err == nil {
			return
		}
		http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	cols, err := db.Database(dbname).ListCollectionNames(ctx, bson.D{})
	if err != nil {
		err = fmt.Errorf("failed to list collections: %w", err)
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
	var all []records
	for _, hostname := range cols {
		col := db.Database(dbname).Collection(hostname)
		paths, err := col.Distinct(ctx, "path", bson.D{})
		if err != nil {
			err = fmt.Errorf("failed to distinct paths: %w", err)
			return
		}
		rs := make([]record, len(paths))
		for i := range rs {
			p := paths[i].(string)
			pv, uv, err := countVisit(ctx, col, p)
			if err != nil {
				err = fmt.Errorf("failed to count visit: %w", err)
				return
			}
			rs[i].Path = p
			rs[i].PV = pv
			rs[i].UV = uv
		}
		all = append(all, records{
			Host:    hostname,
			Records: rs,
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

// countVisit reports the pv and uv of the given hostname collection and path location.
func countVisit(ctx context.Context, col *mongo.Collection, path string) (pv, uv int64, err error) {
	pv, err = col.CountDocuments(ctx, bson.M{"path": path})
	if err != nil {
		return
	}

	result, err := col.Distinct(ctx, "ip", bson.D{
		{Key: "path", Value: bson.D{{Key: "$eq", Value: path}}},
	})
	if err != nil {
		return
	}
	uv = int64(len(result))
	return
}