// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"log"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type allowed struct {
	Production bool     `yaml:"production"`
	Domain     []string `yaml:"domain"`
	GitHub     []string `yaml:"github"`
}

func (a *allowed) isAllowed(source string, isDomain bool) bool {
	allow := false
	if isDomain {
		for idx := range a.Domain {
			if strings.Contains(source, a.Domain[idx]) {
				allow = true
				break
			}
		}
	} else {
		for idx := range a.GitHub {
			if strings.Contains(source, a.GitHub[idx]) {
				allow = true
				break
			}
		}
	}
	return allow
}

var source = &allowed{}

func init() {
	d, err := os.ReadFile("./allowed.yml")
	if err != nil {
		log.Fatalf("failed to load trusted sources: %v", err)
	}

	err = yaml.Unmarshal(d, source)
	if err != nil {
		log.Fatalf("failed to parse trusted sources: %v", err)
	}

	if !source.Production {
		source.Domain = append(source.Domain, "http://localhost")
		source.Domain = append(source.Domain, "http://0.0.0.0")
	}
}
