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

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	//go:embed public/*
	public   embed.FS
	publicFS fs.FS
	l        *log.Logger
	db       *pgxpool.Pool
)

const (
	defaultDBURI = "postgres://urlstat:urlstat@urlstatdb:5432/urlstat?sslmode=disable"
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
	dbURI := os.Getenv("URLSTAT_DB")
	if dbURI == "" {
		dbURI = defaultDBURI
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err = pgxpool.New(ctx, dbURI)
	if err != nil {
		l.Fatalf("cannot connect to database: %v", err)
	}

	if err := db.Ping(ctx); err != nil {
		l.Fatalf("cannot ping database: %v", err)
	}
	log.Printf("connected to database %v", dbURI)
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
