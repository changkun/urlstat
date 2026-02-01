// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BenchmarkCount benchmarks the countVisit function against PostgreSQL.
// Requires a local PostgreSQL instance at postgres://urlstat:urlstat@localhost:5432/urlstat
func BenchmarkCount(b *testing.B) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "postgres://urlstat:urlstat@localhost:5432/urlstat?sslmode=disable")
	if err != nil {
		b.Fatalf("failed to connect to database: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		b.Fatalf("failed to ping database: %v", err)
	}

	// Temporarily replace global db for benchmark
	oldDB := db
	db = pool
	defer func() { db = oldDB }()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _, err := countVisit(context.Background(), "localhost", "/urlstat/dashboard", "page")
			if err != nil {
				b.Fatalf("count failed: %v", err)
			}
		}
	})
}
