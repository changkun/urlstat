# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

urlstat is a lightweight page view (PV) and unique visitor (UV) statistics tracking service with two modes:
- **Plain Mode**: JavaScript-based tracking for websites
- **GitHub Mode**: SVG badge rendering for repository view counts

## Build Commands

```bash
make all      # Build binary locally
make run      # Run the binary with -s flag (production mode)
make build    # Build Linux binary and Docker image
make up       # Start Docker compose services
make down     # Stop Docker compose services
```

## Testing

```bash
go test ./...           # Run all tests
go test -bench . ./...  # Run benchmarks
```

Tests require a local PostgreSQL instance at `postgres://urlstat:urlstat@localhost:5432/urlstat`.

## Architecture

```
HTTP Server (urlstat.go)
├── /urlstat           → PV/UV recording (handlers.go)
├── /urlstat/dashboard → Statistics dashboard (dashboard.go)
├── /urlstat/cleanup   → Cleanup low-visit entries (dashboard.go)
├── /urlstat/client.js → Static JS client
└── GitHub badge mode  → github.go + renderer.go
```

**Data flow**: All visits stored in a single PostgreSQL `visits` table with `hostname` column to distinguish origins. Visit records store `hostname`, `visitor_id`, `path`, `ip`, `ua`, `referer`, `created_at`. Statistics computed via SQL GROUP BY queries.

**Access control**: Domain whitelist in `allowed.yml`. GitHub mode validates requests from GitHub's camo proxy. The `-s` flag enables production mode (disables localhost).

## Database Schema

```sql
CREATE TABLE visits (
    id BIGSERIAL PRIMARY KEY,
    hostname VARCHAR(255) NOT NULL,
    visitor_id UUID NOT NULL,
    path TEXT NOT NULL,
    ip INET NOT NULL,
    ua TEXT,
    referer TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

Indexes are defined in `migrations/001_initial.sql`.

## Migration from MongoDB

Use the migration tool to copy data from MongoDB to PostgreSQL:

```bash
go run ./cmd/migrate -mongo="mongodb://localhost:27017" -pg="postgres://urlstat:urlstat@localhost:5432/urlstat"
```

## Deployment

Docker-based with Alpine Linux. Uses `URLSTAT_DB` environment variable for PostgreSQL connection (defaults to `postgres://urlstat:urlstat@urlstatdb:5432/urlstat?sslmode=disable`). Uses `URLSTAT_ADDR` environment variable (defaults to `0.0.0.0:80`).
