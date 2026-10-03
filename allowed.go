// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"log"
	"os"

	"gopkg.in/yaml.v3"
)

// allowed is allowed.yml: the sources urlstat starts out with. The list
// itself lives in the database (see sources.go) and is filled from this file
// only once, when it is empty. Production stays in force: without it a page
// on this machine may report visits.
type allowed struct {
	Production bool     `yaml:"production"`
	Domain     []string `yaml:"domain"`
	GitHub     []string `yaml:"github"`
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

}
