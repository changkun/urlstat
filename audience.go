// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

//go:embed migrations/004_agents.sql
var agentsSchema string

// unreadAgentsSQL finds the user agent strings of a period that the current
// rules have not read yet.
const unreadAgentsSQL = `
	SELECT d.ua FROM (
		SELECT DISTINCT v.ua FROM visits v WHERE ` + scopeSQL + ` AND v.ua IS NOT NULL
	) d
	WHERE NOT EXISTS (SELECT 1 FROM agents a WHERE a.hash = md5(d.ua) AND a.rules = $5)`

const storeAgentsSQL = `
	INSERT INTO agents (hash, ua, browser, os, device, rules)
	SELECT md5(u), u, b, o, d, $5
	FROM unnest($1::text[], $2::text[], $3::text[], $4::text[]) AS t(u, b, o, d)
	ON CONFLICT (hash) DO UPDATE
	SET browser = EXCLUDED.browser, os = EXCLUDED.os, device = EXCLUDED.device, rules = EXCLUDED.rules`

// agentsRead is how far back each host's user agent strings are known to
// have been read, since this process started. A visit's string is read when
// the visit is recorded (rememberAgent), so only what was recorded before
// needs looking for, and each stretch of a host's past only once.
var agentsRead = struct {
	sync.Mutex
	since map[string]time.Time // host -> everything from here on has been read
	known map[string]bool      // strings read by this process
}{since: map[string]time.Time{}, known: map[string]bool{}}

// knownAgentsLimit bounds the strings remembered as read; past it the
// memory starts over, which costs a few repeated writes.
const knownAgentsLimit = 50000

// rememberAgent reads the user agent string of a visit being recorded, the
// first time this process sees it.
func rememberAgent(ctx context.Context, ua string) {
	if ua == "" {
		return
	}
	agentsRead.Lock()
	seen := agentsRead.known[ua]
	if !seen {
		if len(agentsRead.known) >= knownAgentsLimit {
			clear(agentsRead.known)
		}
		agentsRead.known[ua] = true
	}
	agentsRead.Unlock()
	if seen {
		return
	}
	c := classifyUA(ua)
	if _, err := db.Exec(ctx, storeAgentsSQL, []string{ua}, []string{c.Browser}, []string{c.OS}, []string{c.Device}, agentRules); err != nil {
		l.Printf("failed to store a user agent: %v", err)
	}
}

// readAgentsSince makes sure the user agent strings of a host's visits
// from since on have been read, looking only at the stretch that has not
// been looked at before.
func readAgentsSince(ctx context.Context, hostname string, since time.Time) error {
	agentsRead.Lock()
	read, ok := agentsRead.since[hostname]
	agentsRead.Unlock()
	until := time.Now().UTC()
	if ok {
		if !since.Before(read) {
			return nil
		}
		until = read
	}
	if err := readAgents(ctx, hostname, period{since: since, until: until}, ""); err != nil {
		return err
	}
	agentsRead.Lock()
	if cur, ok := agentsRead.since[hostname]; !ok || since.Before(cur) {
		agentsRead.since[hostname] = since
	}
	agentsRead.Unlock()
	return nil
}

// readAgents reads the user agent strings of a period that have not been
// read yet, so that the visits can be grouped by what they were.
func readAgents(ctx context.Context, hostname string, p period, prefix string) error {
	rows, err := db.Query(ctx, unreadAgentsSQL, hostname, p.since, p.until, prefix, agentRules)
	if err != nil {
		return err
	}
	var uas, browsers, systems, devices []string
	for rows.Next() {
		var ua string
		if err := rows.Scan(&ua); err != nil {
			rows.Close()
			return err
		}
		c := classifyUA(ua)
		uas, browsers, systems, devices = append(uas, ua), append(browsers, c.Browser), append(systems, c.OS), append(devices, c.Device)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(uas) == 0 {
		return err
	}
	_, err = db.Exec(ctx, storeAgentsSQL, uas, browsers, systems, devices, agentRules)
	return err
}

// audienceSQL groups a period's visits by what made them. An address counts
// once per group, and crawlers are set apart: the browsers, systems and
// devices are those of people.
const audienceSQL = `
	WITH v AS MATERIALIZED (
		SELECT ua, ip, COUNT(*) AS n FROM visits WHERE ` + scopeSQL + `
		GROUP BY ua, ip
	), c AS MATERIALIZED (
		SELECT v.ip, v.n, COALESCE(a.browser, '') AS browser, COALESCE(a.os, '') AS os, COALESCE(a.device, '') AS device
		FROM v LEFT JOIN agents a ON a.hash = md5(v.ua)
	)
	SELECT 'who' AS kind, CASE WHEN device = 'bot' THEN 'crawlers' ELSE 'people' END AS name,
		SUM(n)::bigint AS pv, COUNT(DISTINCT ip) AS uv FROM c GROUP BY 2
	UNION ALL SELECT 'browser', browser, SUM(n)::bigint, COUNT(DISTINCT ip) FROM c WHERE device <> 'bot' GROUP BY 2
	UNION ALL SELECT 'system', os, SUM(n)::bigint, COUNT(DISTINCT ip) FROM c WHERE device <> 'bot' GROUP BY 2
	UNION ALL SELECT 'device', device, SUM(n)::bigint, COUNT(DISTINCT ip) FROM c WHERE device <> 'bot' GROUP BY 2`

// returningSQL counts the addresses of a period and those among them seen
// on more than one day.
const returningSQL = `
	SELECT COUNT(*) FILTER (WHERE days > 1), COUNT(*)
	FROM (
		SELECT ip, COUNT(*) AS days
		FROM (SELECT ip, (created_at AT TIME ZONE 'UTC')::date FROM visits WHERE ` + scopeSQL + ` GROUP BY 1, 2) d
		GROUP BY ip
	) t`

// NamedCount is the visits of one group: a browser, a system, a device.
type NamedCount struct {
	Name string `json:"name"`
	PV   int64  `json:"pv"`
	UV   int64  `json:"uv"`
}

// AudienceResponse says who visited in a period: people and crawlers, what
// the people used, and how many addresses came back on another day.
type AudienceResponse struct {
	People    NamedCount   `json:"people"`
	Crawlers  NamedCount   `json:"crawlers"`
	Browsers  []NamedCount `json:"browsers"`
	Systems   []NamedCount `json:"systems"`
	Devices   []NamedCount `json:"devices"`
	Visitors  int64        `json:"visitors"`
	Returning int64        `json:"returning"`
}

// scopeOf reads what a dashboard request is about: the host, the period and
// the path prefix. These endpoints are asked for one host by name.
func scopeOf(r *http.Request) (hostname string, p period, prefix string, err error) {
	hostname = r.URL.Query().Get("hostname")
	if hostname == "" {
		return "", p, "", errors.New("a hostname is needed")
	}
	prefix = strings.TrimRight(r.URL.Query().Get("prefix"), "/")
	if prefix != "" && !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return hostname, parsePeriod(r.URL.Query(), time.Now()), prefix, nil
}

// audienceAPI returns who visited: see AudienceResponse. The page asks for
// it after the rest, because it reads every visit's user agent and is the
// slowest thing on the dashboard.
func audienceAPI(w http.ResponseWriter, r *http.Request) {
	hostname, p, prefix, err := scopeOf(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	key := "audience\x00" + hostname + "\x00" + p.key() + "\x00" + prefix
	dashboardCache.Lock()
	c, ok := dashboardCache.pages[key]
	dashboardCache.Unlock()
	// What people used shifts slowly, and a long period is slow to read, so
	// it is kept five times as long as the rest.
	if ok && time.Since(c.at) < 5*dashboardTTL(p.days) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(c.val)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	start := time.Now()
	if err := readAgentsSince(ctx, hostname, p.since); err != nil {
		http.Error(w, fmt.Sprintf("failed to read the user agents: %v", err), http.StatusInternalServerError)
		return
	}

	resp := AudienceResponse{Browsers: []NamedCount{}, Systems: []NamedCount{}, Devices: []NamedCount{}}
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
	run("audience", func() error {
		tx, err := db.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "SET LOCAL work_mem = '32MB'"); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, audienceSQL, hostname, p.since, p.until, prefix)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var kind string
			var item NamedCount
			if err := rows.Scan(&kind, &item.Name, &item.PV, &item.UV); err != nil {
				return err
			}
			switch kind {
			case "who":
				if item.Name == "crawlers" {
					resp.Crawlers = item
				} else {
					resp.People = item
				}
			case "browser":
				resp.Browsers = append(resp.Browsers, item)
			case "system":
				resp.Systems = append(resp.Systems, item)
			case "device":
				resp.Devices = append(resp.Devices, item)
			}
		}
		return rows.Err()
	})
	run("returning visitors", func() error {
		return db.QueryRow(ctx, returningSQL, hostname, p.since, p.until, prefix).Scan(&resp.Returning, &resp.Visitors)
	})
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp.People.Name, resp.Crawlers.Name = "people", "crawlers"
	busiest := func(a, b NamedCount) int { return cmp.Or(cmp.Compare(b.PV, a.PV), cmp.Compare(a.Name, b.Name)) }
	for _, list := range [][]NamedCount{resp.Browsers, resp.Systems, resp.Devices} {
		slices.SortFunc(list, busiest)
	}
	log.Printf("audience for host %v, %d days, prefix %q took %v", hostname, p.days, prefix, time.Since(start))

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
