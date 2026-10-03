// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"net"
	"net/url"
	"regexp"
	"strings"
)

// Where a visit came from, besides the name of a site or a campaign.
const (
	fromDirect   = ""         // the browser named nothing: a typed or saved address, or an app that does not say
	fromInternal = "internal" // another page of the same site
)

// One site under many addresses, named by its main one. Apps open links
// with their package name where a site would be.
var fromAlias = map[string]string{
	"t.co": "x.com", "twitter.com": "x.com", "com.twitter.android": "x.com",
	"lnkd.in": "linkedin.com", "com.linkedin.android": "linkedin.com",
	"com.google.android.googlequicksearchbox": "google.com",
	"com.google.android.gm":                   "mail.google.com",
	"org.telegram.messenger":                  "t.me", "web.telegram.org": "t.me", "telegram.org": "t.me",
	"mp.weixin.qq.com": "weixin.qq.com", "servicewechat.com": "weixin.qq.com", "com.tencent.mm": "weixin.qq.com",
	"weibo.cn": "weibo.com", "t.cn": "weibo.com",
	"link.zhihu.com":       "zhihu.com",
	"cn.bing.com":          "bing.com",
	"com.reddit.frontpage": "reddit.com", "redd.it": "reddit.com",
	"com.facebook.katana": "facebook.com", "fb.me": "facebook.com",
	"hn.algolia.com": "news.ycombinator.com",
}

// A campaign tag that is the name of a site means that site.
var fromTag = map[string]string{
	"linkedin": "linkedin.com", "twitter": "x.com", "x": "x.com", "github": "github.com",
	"hn": "news.ycombinator.com", "hackernews": "news.ycombinator.com", "google": "google.com",
	"wechat": "weixin.qq.com", "weixin": "weixin.qq.com", "weibo": "weibo.com", "zhihu": "zhihu.com",
	"telegram": "t.me", "facebook": "facebook.com", "fb": "facebook.com", "reddit": "reddit.com",
}

var (
	// google.de, google.co.jp and the rest are one search engine.
	googleRE = regexp.MustCompile(`^google\.[a-z]{2,3}(\.[a-z]{2})?$`)
	yandexRE = regexp.MustCompile(`^yandex\.[a-z]{2,3}(\.[a-z]{2})?$`)
	tagRE    = regexp.MustCompile(`[^a-z0-9._-]+`)
)

// Prefixes that are the same site on another screen or behind a redirector.
var fromPrefixes = []string{"www.", "m.", "l.", "lm.", "mobile.", "old.", "out.", "amp."}

// cameFrom names where the visit to page came from, given the address the
// browser says led there (document.referrer; empty when it says nothing).
//
// Another page of the same site is internal. Otherwise a campaign tag in
// the page's address wins, since whoever shared the link put it there and
// apps often withhold the referrer; then the referring site; and with
// neither, the visit is direct.
func cameFrom(page *url.URL, ref string) string {
	from := refHost(ref)
	if from != "" && bareHost(from) == bareHost(page.Host) {
		return fromInternal
	}
	q := page.Query()
	for _, key := range []string{"utm_source", "ref", "source"} {
		if tag := tagRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(q.Get(key))), ""); tag != "" {
			if len(tag) > 64 {
				tag = tag[:64]
			}
			if site, ok := fromTag[tag]; ok {
				return site
			}
			return tag
		}
	}
	if from == "" {
		return fromDirect
	}
	return siteName(from)
}

// refHost returns the host a referrer names, or "" when it names none.
func refHost(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || len(ref) > 2048 {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if _, ok := siteOf(host); !ok {
		return ""
	}
	return host
}

// bareHost strips the port and a leading www, for telling whether two
// addresses are the same site.
func bareHost(host string) string {
	host = strings.ToLower(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimPrefix(host, "www.")
}

// siteName reduces a referring host to the name its site is counted under.
func siteName(host string) string {
	if site, ok := fromAlias[host]; ok {
		return site
	}
	for _, p := range fromPrefixes {
		if rest, ok := strings.CutPrefix(host, p); ok && strings.Contains(rest, ".") {
			host = rest
			break
		}
	}
	if site, ok := fromAlias[host]; ok {
		return site
	}
	switch {
	case googleRE.MatchString(host):
		return "google.com"
	case yandexRE.MatchString(host):
		return "yandex.com"
	}
	return host
}
