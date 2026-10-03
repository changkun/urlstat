// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import "testing"

func TestClassifyUA(t *testing.T) {
	for _, tt := range []struct {
		ua   string
		want agentClass
	}{
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36",
			agentClass{"Chrome", "macOS", "desktop"}},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Safari/605.1.15",
			agentClass{"Safari", "macOS", "desktop"}},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36 Edg/153.0.0.0",
			agentClass{"Edge", "Windows", "desktop"}},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:142.0) Gecko/20100101 Firefox/142.0",
			agentClass{"Firefox", "Linux", "desktop"}},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Mobile/15E148 Safari/604.1",
			agentClass{"Safari", "iOS", "mobile"}},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 MicroMessenger/8.0.50",
			agentClass{"MicroMessenger", "iOS", "mobile"}},
		{"Mozilla/5.0 (iPad; CPU OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Mobile/15E148 Safari/604.1",
			agentClass{"Safari", "iOS", "tablet"}},
		{"Mozilla/5.0 (Linux; Android 16; Pixel 10) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Mobile Safari/537.36",
			agentClass{"Chrome", "Android", "mobile"}},
		{"Mozilla/5.0 (Linux; Android 16; SM-X910) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36",
			agentClass{"Chrome", "Android", "tablet"}},
		// Automated, however it presents itself.
		{"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", agentClass{"Googlebot", "", "bot"}},
		{"Mozilla/5.0 (Linux; Android 6.0.1; Nexus 5X Build/MMB29P) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Mobile Safari/537.36 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
			agentClass{"Chrome", "Android", "bot"}},
		{"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/153.0.0.0 Safari/537.36",
			agentClass{"Chrome", "Linux", "bot"}},
		{"Mozilla/5.0 (Windows NT 6.1; WOW64) AppleWebKit/534+ (KHTML, like Gecko) BingPreview/1.0b", agentClass{"", "Windows", "bot"}},
		{"curl/8.4.0", agentClass{"curl", "", "bot"}},
		{"Go-http-client/1.1", agentClass{"Go-http-client", "", "bot"}},
		{"Mozilla/5.0 (compatible; Dataprovider.com)", agentClass{"Dataprovider.com", "", "bot"}},
		// Silence is not evidence either way.
		{"", agentClass{"", "", ""}},
	} {
		if got := classifyUA(tt.ua); got != tt.want {
			t.Errorf("classifyUA(%q) =\n\t%+v, want\n\t%+v", tt.ua, got, tt.want)
		}
	}
}
