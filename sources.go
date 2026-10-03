// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// The two kinds of source: a site that loads client.js, and a GitHub
// account whose repositories show the badge.
const (
	kindSite   = "site"
	kindGitHub = "github"
)

// The schema changes since the first, which the service applies itself
// when it starts. Each is written to be applied again without effect.
var (
	//go:embed migrations/002_sources.sql
	sourcesSchema string
	//go:embed migrations/003_came_from.sql
	cameFromSchema string
)

// sourceList is who may be counted. It is kept in the database, so that it
// can be changed from the dashboard, and held in memory, so that counting a
// visit does not ask the database who is allowed.
//
// It also remembers who was turned away since the service started. A site
// that is not on the list is refused without a trace anywhere else, and the
// dashboard offers these to be allowed.
type sourceList struct {
	mu     sync.RWMutex
	sites  map[string]bool
	github map[string]bool
	dev    bool // not production: a page on this machine may report

	refusedMu sync.Mutex
	refused   map[string]*refusal
}

type refusal struct {
	Kind     string    `json:"kind"`
	Value    string    `json:"value"`
	Attempts int64     `json:"attempts"`
	LastSeen time.Time `json:"last_seen"`
}

// refusedLimit bounds what is remembered: anyone can claim to be any site.
const refusedLimit = 200

var sources = &sourceList{sites: map[string]bool{}, github: map[string]bool{}, refused: map[string]*refusal{}}

var (
	siteRE   = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)
	githubRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,37}[a-z0-9])?$`)
)

// siteOf reduces what a browser sent or a person typed, an origin, an
// address or a bare host, to the name a site is known by: its host in lower
// case, with its port if it has one.
func siteOf(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 253 || !siteRE.MatchString(s) {
		return "", false
	}
	return s, true
}

// githubOf does the same for a GitHub account, which GitHub compares
// without regard to case.
func githubOf(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://github.com/"), "@")
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	if !githubRE.MatchString(s) {
		return "", false
	}
	return s, true
}

// sourceOf normalizes value as a source of the given kind.
func sourceOf(kind, value string) (string, bool) {
	switch kind {
	case kindSite:
		return siteOf(value)
	case kindGitHub:
		return githubOf(value)
	}
	return "", false
}

// allowsOrigin reports whether the site at origin (or any address on it)
// may report visits. The whole host must match: the list used to be matched
// as a substring, which let https://changkun.de.example.com pass for
// https://changkun.de.
func (s *sourceList) allowsOrigin(origin string) bool {
	site, ok := siteOf(origin)
	if !ok {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.sites[site] {
		return true
	}
	if s.dev {
		host := site
		if h, _, err := net.SplitHostPort(site); err == nil {
			host = h
		}
		return host == "localhost" || host == "0.0.0.0" || host == "127.0.0.1"
	}
	return false
}

// allowsGitHub reports whether the account's repositories may show the badge.
func (s *sourceList) allowsGitHub(account string) bool {
	account, ok := githubOf(account)
	if !ok {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.github[account]
}

// refuse remembers that a source was turned away. What does not look like
// a site or an account is dropped: it could never be allowed.
func (s *sourceList) refuse(kind, value string) {
	value, ok := sourceOf(kind, value)
	if !ok {
		return
	}
	s.refusedMu.Lock()
	defer s.refusedMu.Unlock()
	key := kind + "\x00" + value
	if r, ok := s.refused[key]; ok {
		r.Attempts++
		r.LastSeen = time.Now()
		return
	}
	if len(s.refused) >= refusedLimit {
		// Full: the one not seen for the longest makes room.
		oldest := ""
		for k, r := range s.refused {
			if oldest == "" || r.LastSeen.Before(s.refused[oldest].LastSeen) {
				oldest = k
			}
		}
		delete(s.refused, oldest)
	}
	s.refused[key] = &refusal{Kind: kind, Value: value, Attempts: 1, LastSeen: time.Now()}
}

// refusals returns who was turned away, most attempts first.
func (s *sourceList) refusals() []refusal {
	s.refusedMu.Lock()
	defer s.refusedMu.Unlock()
	list := make([]refusal, 0, len(s.refused))
	for _, r := range s.refused {
		list = append(list, *r)
	}
	slices.SortFunc(list, func(a, b refusal) int {
		return cmp.Or(cmp.Compare(b.Attempts, a.Attempts), cmp.Compare(a.Value, b.Value))
	})
	return list
}

func (s *sourceList) forget(kind, value string) {
	s.refusedMu.Lock()
	delete(s.refused, kind+"\x00"+value)
	s.refusedMu.Unlock()
}

// load reads the list from the database.
func (s *sourceList) load(ctx context.Context) error {
	rows, err := db.Query(ctx, `SELECT kind, value FROM sources`)
	if err != nil {
		return fmt.Errorf("failed to load the sources: %w", err)
	}
	defer rows.Close()
	sites, github := map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var kind, value string
		if err := rows.Scan(&kind, &value); err != nil {
			return fmt.Errorf("failed to load the sources: %w", err)
		}
		switch kind {
		case kindSite:
			sites[value] = true
		case kindGitHub:
			github[value] = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to load the sources: %w", err)
	}
	s.mu.Lock()
	s.sites, s.github = sites, github
	s.mu.Unlock()
	return nil
}

// setupSources makes sure the table exists, fills it from allowed.yml the
// first time, and loads it. From then on allowed.yml is not read again for
// the list: the dashboard is where it changes.
func setupSources(ctx context.Context, seed *allowed) error {
	if _, err := db.Exec(ctx, sourcesSchema); err != nil {
		return fmt.Errorf("failed to create the sources table: %w", err)
	}
	if _, err := db.Exec(ctx, cameFromSchema); err != nil {
		return fmt.Errorf("failed to add where visits came from: %w", err)
	}
	if _, err := db.Exec(ctx, agentsSchema); err != nil {
		return fmt.Errorf("failed to create the agents table: %w", err)
	}
	var n int64
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM sources`).Scan(&n); err != nil {
		return fmt.Errorf("failed to count the sources: %w", err)
	}
	if n == 0 {
		add := func(kind string, values []string) error {
			for _, v := range values {
				value, ok := sourceOf(kind, v)
				if !ok {
					l.Printf("allowed.yml: %q is not a %s, skipped", v, kind)
					continue
				}
				if _, err := db.Exec(ctx, `INSERT INTO sources (kind, value, added_by) VALUES ($1, $2, 'allowed.yml')
					ON CONFLICT DO NOTHING`, kind, value); err != nil {
					return fmt.Errorf("failed to import %s from allowed.yml: %w", v, err)
				}
			}
			return nil
		}
		if err := cmp.Or(add(kindSite, seed.Domain), add(kindGitHub, seed.GitHub)); err != nil {
			return err
		}
		l.Printf("imported the sources from allowed.yml")
	}
	sources.mu.Lock()
	sources.dev = !seed.Production
	sources.mu.Unlock()
	return sources.load(ctx)
}

// refreshSources picks up changes made elsewhere, another replica or a
// hand in the database, within a minute.
func refreshSources() {
	for range time.Tick(time.Minute) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if err := sources.load(ctx); err != nil {
			l.Println(err)
		}
		cancel()
	}
}

type sourceItem struct {
	Value   string    `json:"value"`
	PV      int64     `json:"pv"` // visits over the last sourcesDays
	AddedBy string    `json:"added_by"`
	AddedAt time.Time `json:"added_at"`
}

type sourcesResponse struct {
	Changed string       `json:"changed,omitempty"` // the source a POST or DELETE named, as it is stored
	Days    int          `json:"days"`
	Sites   []sourceItem `json:"sites"`
	GitHub  []sourceItem `json:"github"`
	Refused []refusal    `json:"refused"`
}

const sourcesDays = 30

// A badge's visit is stored under the repository's address,
// https://github.com/account/repository, whose fourth part is the account.
const githubViewsSQL = `
	SELECT lower(split_part(path, '/', 4)), COUNT(*) FROM visits
	WHERE hostname = 'github.com' AND created_at >= $1 GROUP BY 1`

// sourcesAPI lists the sources (GET), allows one (POST) or stops counting
// one (DELETE). It is mounted behind admin. Stopping keeps the visits a
// source already has.
func sourcesAPI(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	changed := ""
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost, http.MethodDelete:
		var req struct {
			Kind  string `json:"kind"`
			Value string `json:"value"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("cannot read the request: %v", err), http.StatusBadRequest)
			return
		}
		value, ok := sourceOf(req.Kind, req.Value)
		if !ok {
			what := map[string]string{kindSite: "a site, such as example.com", kindGitHub: "a GitHub account"}[req.Kind]
			if what == "" {
				http.Error(w, "a source is a site or a github account", http.StatusBadRequest)
				return
			}
			http.Error(w, fmt.Sprintf("%q is not %s", req.Value, what), http.StatusBadRequest)
			return
		}
		var err error
		if r.Method == http.MethodPost {
			_, err = db.Exec(ctx, `INSERT INTO sources (kind, value, added_by) VALUES ($1, $2, $3)
				ON CONFLICT DO NOTHING`, req.Kind, value, principalOf(r))
		} else {
			_, err = db.Exec(ctx, `DELETE FROM sources WHERE kind = $1 AND value = $2`, req.Kind, value)
		}
		if err == nil {
			err = sources.load(ctx)
		}
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to change the sources: %v", err), http.StatusInternalServerError)
			return
		}
		if r.Method == http.MethodPost {
			sources.forget(req.Kind, value)
		}
		changed = value
		l.Printf("sources: %s %s %s by %s", r.Method, req.Kind, value, principalOf(r))
	default:
		http.Error(w, "sources take a GET, a POST or a DELETE", http.StatusMethodNotAllowed)
		return
	}

	resp, err := listSources(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp.Changed = changed
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(resp)
}

// listSources returns the sources with their recent visits, and who was
// turned away.
func listSources(ctx context.Context) (*sourcesResponse, error) {
	now := time.Now().UTC()
	since := now.Truncate(24*time.Hour).AddDate(0, 0, -(sourcesDays - 1))

	// Visits per site come from the dashboard's host list.
	views := map[string]int64{}
	hosts, err := listHosts(ctx, period{since: since, until: now, days: sourcesDays})
	if err != nil {
		return nil, err
	}
	for _, h := range hosts {
		views[kindSite+"\x00"+h.Hostname] = h.PV
	}
	rows, err := db.Query(ctx, githubViewsSQL, since)
	if err != nil {
		return nil, fmt.Errorf("failed to count the badge views: %w", err)
	}
	for rows.Next() {
		var account string
		var pv int64
		if err := rows.Scan(&account, &pv); err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed to count the badge views: %w", err)
		}
		views[kindGitHub+"\x00"+account] = pv
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to count the badge views: %w", err)
	}

	resp := &sourcesResponse{Days: sourcesDays, Sites: []sourceItem{}, GitHub: []sourceItem{}, Refused: sources.refusals()}
	rows, err = db.Query(ctx, `SELECT kind, value, added_by, added_at FROM sources ORDER BY value`)
	if err != nil {
		return nil, fmt.Errorf("failed to list the sources: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var item sourceItem
		if err := rows.Scan(&kind, &item.Value, &item.AddedBy, &item.AddedAt); err != nil {
			return nil, fmt.Errorf("failed to list the sources: %w", err)
		}
		item.PV = views[kind+"\x00"+item.Value]
		if kind == kindSite {
			resp.Sites = append(resp.Sites, item)
		} else {
			resp.GitHub = append(resp.GitHub, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list the sources: %w", err)
	}
	// Busiest first, so that a source nobody uses any more sinks to the end.
	busiest := func(a, b sourceItem) int { return cmp.Or(cmp.Compare(b.PV, a.PV), cmp.Compare(a.Value, b.Value)) }
	slices.SortFunc(resp.Sites, busiest)
	slices.SortFunc(resp.GitHub, busiest)
	return resp, nil
}
