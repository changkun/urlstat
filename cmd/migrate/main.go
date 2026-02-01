// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Command migrate copies data from MongoDB to PostgreSQL.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var (
	mongoURI = flag.String("mongo", "mongodb://localhost:27017", "MongoDB connection URI")
	pgURI    = flag.String("pg", "postgres://urlstat:urlstat@localhost:5432/urlstat?sslmode=disable", "PostgreSQL connection URI")
	dbName   = flag.String("db", "urlstat", "MongoDB database name")
	batch    = flag.Int("batch", 10000, "Batch size for bulk inserts")
)

type visit struct {
	VisitorID string    `bson:"visitor_id"`
	Path      string    `bson:"path"`
	IP        string    `bson:"ip"`
	UA        string    `bson:"ua"`
	Referer   string    `bson:"referer"`
	Time      time.Time `bson:"time"`
}

func main() {
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	ctx := context.Background()

	// Connect to MongoDB
	log.Printf("Connecting to MongoDB: %s", *mongoURI)
	mongoClient, err := mongo.Connect(ctx, options.Client().ApplyURI(*mongoURI))
	if err != nil {
		log.Fatalf("Failed to connect to MongoDB: %v", err)
	}
	defer mongoClient.Disconnect(ctx)

	if err := mongoClient.Ping(ctx, nil); err != nil {
		log.Fatalf("Failed to ping MongoDB: %v", err)
	}
	log.Println("Connected to MongoDB")

	// Connect to PostgreSQL
	log.Printf("Connecting to PostgreSQL: %s", *pgURI)
	pgPool, err := pgxpool.New(ctx, *pgURI)
	if err != nil {
		log.Fatalf("Failed to connect to PostgreSQL: %v", err)
	}
	defer pgPool.Close()

	if err := pgPool.Ping(ctx); err != nil {
		log.Fatalf("Failed to ping PostgreSQL: %v", err)
	}
	log.Println("Connected to PostgreSQL")

	// Get list of collections (each represents a hostname)
	mongoDB := mongoClient.Database(*dbName)
	collections, err := mongoDB.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		log.Fatalf("Failed to list collections: %v", err)
	}
	log.Printf("Found %d collections to migrate", len(collections))

	var totalMigrated int64
	for _, hostname := range collections {
		migrated, err := migrateCollection(ctx, mongoDB.Collection(hostname), pgPool, hostname)
		if err != nil {
			log.Printf("ERROR migrating %s: %v", hostname, err)
			continue
		}
		totalMigrated += migrated
	}

	log.Printf("Migration complete. Total documents migrated: %d", totalMigrated)

	// Verify counts
	log.Println("Verifying counts...")
	verifyMigration(ctx, mongoDB, pgPool, collections)
}

func migrateCollection(ctx context.Context, col *mongo.Collection, pgPool *pgxpool.Pool, hostname string) (int64, error) {
	start := time.Now()
	log.Printf("Starting migration for %s", hostname)

	// Count documents in MongoDB
	mongoCount, err := col.CountDocuments(ctx, bson.D{})
	if err != nil {
		return 0, fmt.Errorf("failed to count documents: %w", err)
	}
	log.Printf("%s: %d documents to migrate", hostname, mongoCount)

	if mongoCount == 0 {
		return 0, nil
	}

	// Stream documents from MongoDB
	cursor, err := col.Find(ctx, bson.D{})
	if err != nil {
		return 0, fmt.Errorf("failed to find documents: %w", err)
	}
	defer cursor.Close(ctx)

	var migrated int64
	var rows [][]any

	for cursor.Next(ctx) {
		var v visit
		if err := cursor.Decode(&v); err != nil {
			log.Printf("WARNING: failed to decode document: %v", err)
			continue
		}

		// Generate UUID for empty visitor_id (PostgreSQL requires valid UUID)
		visitorID := v.VisitorID
		if visitorID == "" {
			visitorID = uuid.New().String()
		}

		// Sanitize all string fields to remove invalid UTF-8
		rows = append(rows, []any{
			sanitizeUTF8(hostname),
			visitorID,
			sanitizeUTF8(v.Path),
			sanitizeUTF8(v.IP),
			sanitizeUTF8(v.UA),
			sanitizeUTF8(v.Referer),
			v.Time,
		})

		if len(rows) >= *batch {
			inserted, err := bulkInsert(ctx, pgPool, rows)
			if err != nil {
				return migrated, fmt.Errorf("bulk insert failed: %w", err)
			}
			migrated += inserted
			log.Printf("%s: migrated %d/%d documents", hostname, migrated, mongoCount)
			rows = rows[:0]
		}
	}

	if err := cursor.Err(); err != nil {
		return migrated, fmt.Errorf("cursor error: %w", err)
	}

	// Insert remaining rows
	if len(rows) > 0 {
		inserted, err := bulkInsert(ctx, pgPool, rows)
		if err != nil {
			return migrated, fmt.Errorf("bulk insert failed: %w", err)
		}
		migrated += inserted
	}

	log.Printf("%s: migration complete. Migrated %d documents in %v", hostname, migrated, time.Since(start))
	return migrated, nil
}

func bulkInsert(ctx context.Context, pgPool *pgxpool.Pool, rows [][]any) (int64, error) {
	copyCount, err := pgPool.CopyFrom(
		ctx,
		pgx.Identifier{"visits"},
		[]string{"hostname", "visitor_id", "path", "ip", "ua", "referer", "created_at"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return 0, err
	}
	return copyCount, nil
}

// sanitizeUTF8 removes invalid UTF-8 sequences from a string
func sanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	// Replace invalid sequences with empty string
	var b strings.Builder
	for i, r := range s {
		if r == utf8.RuneError {
			_, size := utf8.DecodeRuneInString(s[i:])
			if size == 1 {
				continue // skip invalid byte
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

func verifyMigration(ctx context.Context, mongoDB *mongo.Database, pgPool *pgxpool.Pool, collections []string) {
	for _, hostname := range collections {
		col := mongoDB.Collection(hostname)
		mongoCount, err := col.CountDocuments(ctx, bson.D{})
		if err != nil {
			log.Printf("ERROR getting MongoDB count for %s: %v", hostname, err)
			continue
		}

		var pgCount int64
		err = pgPool.QueryRow(ctx, "SELECT COUNT(*) FROM visits WHERE hostname = $1", hostname).Scan(&pgCount)
		if err != nil {
			log.Printf("ERROR getting PostgreSQL count for %s: %v", hostname, err)
			continue
		}

		status := "OK"
		if mongoCount != pgCount {
			status = "MISMATCH"
		}
		log.Printf("%s: MongoDB=%d PostgreSQL=%d [%s]", hostname, mongoCount, pgCount, status)
	}

	// Total count
	var totalPG int64
	if err := pgPool.QueryRow(ctx, "SELECT COUNT(*) FROM visits").Scan(&totalPG); err == nil {
		log.Printf("Total PostgreSQL records: %d", totalPG)
	}
}
