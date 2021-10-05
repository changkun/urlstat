// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// FIXME: testable
func BenchmarkCount(b *testing.B) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	db, err := mongo.Connect(ctx,
		options.Client().ApplyURI("mongodb://0.0.0.0:27017"))
	if err != nil {
		b.Fatal(err)
	}
	col := db.Database(dbname).Collection("localhost")

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _, err := countVisit(context.Background(), col, "/urlstat/dashboard")
			if err != nil {
				b.Fatalf("conection failed")
			}
		}
	})
}
