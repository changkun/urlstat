// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"sync"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// indexDefinitions defines the indexes needed for optimal query performance.
// - {path: 1, ip: 1}: compound index for dashboard aggregation and path-filtered queries
// - {ip: 1}: single-field index for site-wide UV distinct queries
var indexDefinitions = []mongo.IndexModel{
	{
		Keys: bson.D{{Key: "path", Value: 1}, {Key: "ip", Value: 1}},
	},
	{
		Keys: bson.D{{Key: "ip", Value: 1}},
	},
}

// ensureIndexes creates the required indexes on a single collection.
func ensureIndexes(ctx context.Context, col *mongo.Collection) error {
	_, err := col.Indexes().CreateMany(ctx, indexDefinitions)
	if err != nil {
		return err
	}
	l.Printf("ensured indexes for collection: %s", col.Name())
	return nil
}

// ensureAllIndexes creates the required indexes on all existing collections in the database.
func ensureAllIndexes(ctx context.Context, database *mongo.Database) error {
	collections, err := database.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		return err
	}

	for _, name := range collections {
		col := database.Collection(name)
		if err := ensureIndexes(ctx, col); err != nil {
			l.Printf("failed to ensure indexes for collection %s: %v", name, err)
			// Continue with other collections even if one fails
		}
	}
	return nil
}

// indexedCollections tracks which collections have had indexes ensured.
var indexedCollections sync.Map

// ensureIndexesOnce ensures indexes for a collection only once per session.
// This is called asynchronously when saving visits to handle new collections.
func ensureIndexesOnce(col *mongo.Collection) {
	name := col.Name()
	if _, loaded := indexedCollections.LoadOrStore(name, true); loaded {
		// Already ensured for this collection
		return
	}

	// Run index creation in background
	go func() {
		ctx := context.Background()
		if err := ensureIndexes(ctx, col); err != nil {
			l.Printf("async index creation failed for %s: %v", name, err)
			// Remove from map so it can be retried
			indexedCollections.Delete(name)
		}
	}()
}
