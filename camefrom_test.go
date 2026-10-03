// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestCameFrom(t *testing.T) {
	for _, tt := range []struct {
		page, ref, want string
	}{
		// Nothing named.
		{"https://changkun.de/bobook/en/", "", fromDirect},
		{"https://changkun.de/bobook/en/", "   ", fromDirect},
		{"https://changkun.de/bobook/en/", "not an address", fromDirect},
		{"https://changkun.de/bobook/en/", "about:blank", fromDirect},
		// The same site, whatever the page, the www or the port.
		{"https://changkun.de/bobook/en/preface.html", "https://changkun.de/bobook/en/", fromInternal},
		{"https://changkun.de/blog/", "https://www.changkun.de/", fromInternal},
		{"http://localhost:8080/a", "http://localhost:8080/b", fromInternal},
		// A subdomain is another site.
		{"https://changkun.de/blog/", "https://blog.changkun.de/posts/x", "blog.changkun.de"},
		// Sites, under their main name.
		{"https://changkun.de/bobook/en/", "https://www.linkedin.com/", "linkedin.com"},
		{"https://changkun.de/bobook/en/", "https://lnkd.in/abc", "linkedin.com"},
		{"https://changkun.de/bobook/en/", "android-app://com.linkedin.android/", "linkedin.com"},
		{"https://changkun.de/bobook/en/", "https://t.co/xyz", "x.com"},
		{"https://changkun.de/bobook/en/", "https://www.google.de/", "google.com"},
		{"https://changkun.de/bobook/en/", "https://www.google.co.jp/search?q=x", "google.com"},
		{"https://changkun.de/bobook/en/", "https://m.baidu.com/from=844b/s?word=x", "baidu.com"},
		{"https://changkun.de/bobook/en/", "https://news.ycombinator.com/item?id=1", "news.ycombinator.com"},
		{"https://changkun.de/bobook/en/", "https://old.reddit.com/r/x", "reddit.com"},
		{"https://changkun.de/bobook/en/", "https://l.facebook.com/l.php?u=x", "facebook.com"},
		{"https://changkun.de/bobook/en/", "https://GitHub.com/changkun/bobook", "github.com"},
		{"https://changkun.de/bobook/en/", "https://m.example.org:8443/page", "example.org"},
		{"https://changkun.de/bobook/en/", "https://m.io/x", "m.io"},
		// A campaign tag: wins over the referring site, not over the same site.
		{"https://changkun.de/bobook/en/?utm_source=linkedin", "", "linkedin.com"},
		{"https://changkun.de/bobook/en/?utm_source=LinkedIn&utm_medium=social", "android-app://com.linkedin.android/", "linkedin.com"},
		{"https://changkun.de/bobook/en/?utm_source=newsletter", "https://mail.google.com/", "newsletter"},
		{"https://changkun.de/bobook/en/?ref=hn", "", "news.ycombinator.com"},
		{"https://changkun.de/bobook/en/?utm_source=My%20List!", "", "mylist"},
		{"https://changkun.de/bobook/en/?utm_source=linkedin", "https://changkun.de/bobook/", fromInternal},
		{"https://changkun.de/bobook/en/?utm_source=" + strings.Repeat("a", 200), "", strings.Repeat("a", 64)},
	} {
		page, err := url.Parse(tt.page)
		if err != nil {
			t.Fatal(err)
		}
		if got := cameFrom(page, tt.ref); got != tt.want {
			t.Errorf("cameFrom(%q, %q) = %q, want %q", tt.page, tt.ref, got, tt.want)
		}
	}
}
