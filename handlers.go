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
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

type stat struct {
	PagePV int64 `json:"page_pv"`
	PageUV int64 `json:"page_uv"`
	SitePV int64 `json:"site_pv"`
	SiteUV int64 `json:"site_uv"`
}

type visit struct {
	VisitorID string    `json:"visitor_id" bson:"visitor_id"`
	Path      string    `json:"path"    bson:"path"`
	IP        string    `json:"ip"      bson:"ip"`
	UA        string    `json:"ua"      bson:"ua"`
	Referer   string    `json:"referer" bson:"referer"`
	Time      time.Time `json:"time"    bson:"time"`
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
	col := db.Database(dbname).Collection(u.Host)
	vid, err = saveVisit(r.Context(), col, &visit{
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
		args := strings.Split(value, " ")
		for _, arg := range args {
			var pv, uv int64
			pv, uv, err = countVisit(r.Context(), col, u.Path, arg)
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

// saveVisit saves a visit to storage.
func saveVisit(ctx context.Context, col *mongo.Collection, v *visit) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()

	// if visitor ID does not present, then generate a new visitor ID.
	if v.VisitorID == "" {
		v.VisitorID = uuid.New().String()
	}

	_, err := col.InsertOne(ctx, v)
	if err != nil {
		err = fmt.Errorf("failed to insert record: %w", err)
		return "", err
	}
	return v.VisitorID, nil
}

// countVisit reports the pv and uv of the given hostname collection and path location.
func countVisit(ctx context.Context, col *mongo.Collection, path string, mode string) (pv int64, uv int64, err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()

	switch mode {
	case "site":
		pv, err = col.CountDocuments(ctx, bson.M{})
		if err != nil {
			return
		}

		var result []interface{}
		result, err = col.Distinct(ctx, "ip", bson.D{})
		if err != nil {
			return
		}
		uv = int64(len(result))
	case "page":
		pv, err = col.CountDocuments(ctx, bson.M{"path": path})
		if err != nil {
			return
		}

		var result []interface{}
		result, err = col.Distinct(ctx, "ip", bson.D{
			{Key: "path", Value: bson.D{{Key: "$eq", Value: path}}},
		})
		if err != nil {
			return
		}
		uv = int64(len(result))
	}

	return
}
