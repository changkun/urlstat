// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"embed"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var (
	//go:embed public/*
	public   embed.FS
	publicFS fs.FS
	l        *log.Logger
	db       *mongo.Client
)

const (
	dbname = "urlstat"
	// FIXME: This service currently depends on an external project for database.
	// We can't afford instances to run two mongodb containers.
	dburi = "mongodb://redirdb:27017"
)

func init() {
	var err error
	// initialize file system
	l = log.New(os.Stdout, "", log.LstdFlags|log.Lshortfile|log.Lmsgprefix)
	publicFS, err = fs.Sub(public, "public")
	if err != nil {
		l.Fatalf("cannot access sub file system: %v", err)
	}

	// initialize database connection
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	db, err = mongo.Connect(ctx, options.Client().ApplyURI(dburi))
	if err != nil {
		l.Fatalf("cannot connect to database: %v", err)
	}
	log.Printf("connected to database %v", dburi)

	// ensure indexes on all existing collections
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := ensureAllIndexes(ctx, db.Database(dbname)); err != nil {
			l.Printf("failed to ensure indexes on startup: %v", err)
		}
	}()
}

func main() {
	r := http.NewServeMux()
	r.HandleFunc("/urlstat", recording)
	r.HandleFunc("/urlstat/dashboard", dashboard)
	r.HandleFunc("/urlstat/cleanup", handleCleanup)
	r.HandleFunc("/urlstat/client.js", func(w http.ResponseWriter, r *http.Request) {
		f, _ := publicFS.Open("client.js")
		b, _ := io.ReadAll(f)
		w.Write(b)
	})

	addr := os.Getenv("URLSTAT_ADDR")
	if len(addr) == 0 {
		addr = "0.0.0.0:80"
	}

	s := &http.Server{
		Addr:         addr,
		Handler:      logging(l)(r),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: time.Minute,
		IdleTimeout:  time.Minute,
	}

	done := make(chan bool)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)

	go func() {
		<-quit
		l.Println("changkun.de/urlstat is shutting down...")

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		s.SetKeepAlivesEnabled(false)
		if err := s.Shutdown(ctx); err != nil {
			l.Fatalf("cannot gracefully shutdown changkun.de/urlstat: %v", err)
		}
		close(done)
	}()

	l.Printf("changkun.de/urlstat is serving on http://%s", addr)
	if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		l.Fatalf("cannot listen on %s, err: %v\n", addr, err)
	}

	l.Println("goodbye!")
	<-done
}
