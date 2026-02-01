// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"sync"
	"time"

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
	// Wait for database connection to be ready
	if err := waitForConnection(ctx, database.Client()); err != nil {
		return err
	}

	collections, err := database.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		return err
	}

	for _, name := range collections {
		col := database.Collection(name)
		if err := ensureIndexesWithRetry(ctx, col, 3); err != nil {
			l.Printf("failed to ensure indexes for collection %s: %v", name, err)
			// Continue with other collections even if one fails
		}
	}
	return nil
}

// waitForConnection pings the database until it responds or context expires.
func waitForConnection(ctx context.Context, client *mongo.Client) error {
	for i := 0; i < 10; i++ {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := client.Ping(pingCtx, nil)
		cancel()
		if err == nil {
			return nil
		}
		l.Printf("waiting for database connection (attempt %d): %v", i+1, err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return client.Ping(ctx, nil)
}

// ensureIndexesWithRetry creates indexes with retry logic for transient failures.
func ensureIndexesWithRetry(ctx context.Context, col *mongo.Collection, maxRetries int) error {
	var err error
	for i := 0; i < maxRetries; i++ {
		err = ensureIndexes(ctx, col)
		if err == nil {
			return nil
		}
		l.Printf("index creation attempt %d failed for %s: %v", i+1, col.Name(), err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(i+1) * 2 * time.Second):
		}
	}
	return err
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
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := ensureIndexesWithRetry(ctx, col, 3); err != nil {
			l.Printf("async index creation failed for %s: %v", name, err)
			// Remove from map so it can be retried
			indexedCollections.Delete(name)
		}
	}()
}
