package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func githubMode(w http.ResponseWriter, r *http.Request) (err error) {
	ua := r.UserAgent()

	// GitHub uses camo, see:
	// https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/about-anonymized-urls
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
	if !source.isAllowed(ss[0], false) {
		err = errors.New("username is not allowed, please contact @changkun")
		return
	}

	// Currently we always perform a reuqest to github and double check
	// if the repo exists. This is necessary because a repo might not
	// exist, moved, or deleted.
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

	var cookieVid string
	c, err := r.Cookie(urlstatCookieVid)
	if err != nil {
		cookieVid = ""
	} else {
		cookieVid = c.Value
	}

	var vid string
	col := db.Database(dbname).Collection("github.com")
	vid, err = saveVisit(r.Context(), col, &visit{
		VisitorID: cookieVid,
		Path:      repoPath,
		IP:        readIP(r),
		UA:        ua,
		Time:      time.Now().UTC(),
	})
	if err != nil {
		err = fmt.Errorf("failed to save visit: %w", err)
		return
	}
	if cookieVid == "" && vid != "" {
		w.Header().Set("Set-Cookie", urlstatCookieVid+"="+vid)
	}

	pv, _, err := countVisit(r.Context(), col, repoPath, "page")
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
