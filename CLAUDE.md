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

Tests require a local MongoDB instance at `mongodb://0.0.0.0:27017`.

## Architecture

```
HTTP Server (urlstat.go)
├── /urlstat           → PV/UV recording (handlers.go)
├── /urlstat/dashboard → Statistics dashboard (dashboard.go)
├── /urlstat/client.js → Static JS client
└── GitHub badge mode  → github.go + renderer.go
```

**Data flow**: Each hostname gets its own MongoDB collection. Visit documents store `visitor_id`, `path`, `ip`, `ua`, `referer`, `time`. Statistics computed via MongoDB aggregation pipeline.

**Access control**: Domain whitelist in `allowed.yml`. GitHub mode validates requests from GitHub's camo proxy. The `-s` flag enables production mode (disables localhost).

## Deployment

Docker-based with Alpine Linux. Requires external MongoDB at `mongodb://redirdb:27017`. Uses `URLSTAT_ADDR` environment variable (defaults to `0.0.0.0:80`).
