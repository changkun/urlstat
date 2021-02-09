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
	"https://github.com",
	"https://www.github.com",
	"http://www.medien.ifi.lmu.de",
	"https://www.medien.ifi.lmu.de",
	"https://qcrao.com",
}

func isOriginAlloed(origin string) bool {
	allow := false
	for idx := range allowedOrigin {
		if strings.Contains(origin, allowedOrigin[idx]) {
			allow = true
			break
		}
	}
	return allow
}

// recording implmenets a very basic pv/uv statistic function. client script
// is distributed from /urlstat/client.js endpoint.
func recording(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		if isOriginAlloed(origin) {
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
	if !isOriginAlloed(ori) {
		err = errors.New("origin not allowed")
		return
	}

	// Save reported statistics to database
	col := db.Database(dbname).Collection(u.Host)
	err = saveVisit(r.Context(), col, &visit{
		Path: u.Path,
		IP:   readIP(r),
		UA:   r.Header.Get("urlstat-ua"),
		Time: time.Now().UTC(),
	})
	if err != nil {
		err = fmt.Errorf("failed to save visit: %w", err)
		return
	}

	// Report existing statistics
	pv, uv, err := countVisit(r.Context(), col, u.Path)
	if err != nil {
		err = fmt.Errorf("failed to count user view count: %w", err)
		return
	}
	b, _ := json.Marshal(stat{PagePV: pv, PageUV: uv})
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
	return
}

// TODO: allow more origins, and use regexp for matching.
var allowedGitHubUsers = []string{
	// Users
	"changkun",
	"ouchangkun",
	"yangwenmai",
	"maiyang",
	"qcrao",
	"aofei",

	// Organizations
	"mimuc",
	"golang-design",
	"talkgo",
	"talkgofm",
}

func githubMode(w http.ResponseWriter, r *http.Request) (err error) {
	ua := r.Header.Get("User-Agent")

	// GitHub uses camo, see:
	// https://docs.github.com/en/github/authenticating-to-github/about-anonymized-image-urls
	if !strings.Contains(ua, "github-camo") {
		err = errors.New("origin not allowed, require github")
		return
	}

	locs, ok := r.URL.Query()["repo"]
	if !ok {
		err = errors.New("missing location query parameter")
		return
	}
	loc := locs[0]
	ss := strings.Split(loc, "/")
	if len(ss) != 2 {
		err = errors.New("invalid input, require username/repo")
		return
	}

	// Only allow specified users, maybe allow more in the future.
	allowed := false
	for idx := range allowedGitHubUsers {
		if strings.Compare(ss[0], allowedGitHubUsers[idx]) == 0 {
			allowed = true
			break
		}
	}
	if !allowed {
		err = errors.New("username is not allowed, please contact @changkun")
		return
	}

	// FIXME: maybe optimize here. Currently we always perform a reuqest
	// to github and double check if the repo exists. This is necessary
	// because a repo might not exist, moved, or deleted.
	repoPath := fmt.Sprintf("%s/%s", "https://github.com", loc)
	resp, err := http.Get(repoPath)
	if err != nil {
		err = fmt.Errorf("failed to request github: %w", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK &&
		resp.StatusCode != http.StatusMovedPermanently {
		err = fmt.Errorf("%s is not a GitHub repository", repoPath)
		return
	}
	// Figure out the new location if the repo is moved
	if resp.StatusCode == http.StatusMovedPermanently {
		repoPath = resp.Header.Get("Location")
	}

	col := db.Database(dbname).Collection("github.com")
	err = saveVisit(r.Context(), col, &visit{
		Path: repoPath,
		IP:   readIP(r),
		UA:   ua,
		Time: time.Now().UTC(),
	})
	if err != nil {
		err = fmt.Errorf("failed to save visit: %w", err)
		return
	}

	pv, _, err := countVisit(r.Context(), col, repoPath)
	if err != nil {
		err = fmt.Errorf("failed to count visit: %w", err)
		return
	}

	badge, err := drawer.RenderBytes("visitors", fmt.Sprintf("%d", pv), colorBlue)
	if err != nil {
		err = fmt.Errorf("failed to render stat badge: %w", err)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write(badge)
	return nil
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

// saveVisit saves a visit to storage.
func saveVisit(ctx context.Context, col *mongo.Collection, v *visit) (err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()

	_, err = col.InsertOne(ctx, v)
	if err != nil {
		err = fmt.Errorf("failed to insert record: %w", err)
		return
	}
	return
}

// countVisit reports the pv and uv of the given hostname collection and path location.
func countVisit(ctx context.Context, col *mongo.Collection, path string) (pv, uv int64, err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()

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
