# urlstat

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

Tests require a local PostgreSQL instance at `postgres://urlstat:urlstat@localhost:5432/urlstat` with `migrations/001_initial.sql` applied. The package connects to its database when it starts, so point it there as well: `URLSTAT_DB='postgres://urlstat:urlstat@localhost:5432/urlstat?sslmode=disable' go test ./...`.

## Architecture

```
HTTP Server (urlstat.go)
├── /urlstat           → PV/UV recording (handlers.go)
├── /urlstat/dashboard → Statistics dashboard (dashboard.go, public/dashboard.html)
├── /urlstat/dashboard/api → The dashboard's data as JSON (dashboard.go)
├── /urlstat/dashboard/session → Who is signed in (auth.go)
├── /urlstat/dashboard/cleanup → Preview or delete visits, signed in only (cleanup.go)
├── /urlstat/dashboard/sources → List, allow or stop counting a source, signed in only (sources.go)
├── /urlstat/client.js → Static JS client
└── GitHub badge mode  → github.go + renderer.go
```

**Data flow**: All visits stored in a single PostgreSQL `visits` table with `hostname` column to distinguish origins. Visit records store `hostname`, `visitor_id`, `path`, `ip`, `ua`, `referer`, `created_at`. Statistics computed via SQL GROUP BY queries.

**Dashboard**: `public/dashboard.html` is one file with no external scripts; it draws its chart as inline SVG and reads everything from `/urlstat/dashboard/api?hostname=&days=&prefix=`, or `from=&to=` in place of `days` for a range of days chosen by dragging across the chart. The API returns the hosts (busiest first, which is also the default host), and for one host the totals for the period and the one before it, daily counts, the pages grouped by their next path segment, and the top pages. `prefix` narrows all of that to the pages under one path, matched by whole segments, which is how a section such as `/bobook` is totalled. Visitors are distinct IP addresses over the period, counted in two grouping steps because `COUNT(DISTINCT ip)` sorts and the server has one processor. Responses are cached in memory, a minute for a month and longer for longer periods.

**Managing the statistics**: deleting visits takes a latere login, the one the main site and the blog use. The dashboard loads `https://changkun.de/login-sdk.js` (served by changkun/main), which runs PKCE against auth.latere.ai and keeps the access token in the origin's local storage, so being signed in on changkun.de is being signed in on the dashboard. Signing in from the dashboard itself needs `https://changkun.de/urlstat/dashboard` among the redirect URIs of the `changkun-blog` client. The server verifies the token against the issuer's JWKS (`auth.go`, with `latere.ai/x/pkg/authkit`) and then checks `AUTH_ALLOWED_PRINCIPALS`: a valid token proves identity, not the right to delete, and with no allowlist nobody is admitted. `POST /urlstat/dashboard/cleanup` takes `{hostname, paths}` or `{hostname, below, prefix}` and only counts what it would delete unless `confirm` is set; it deletes over all time, not the period shown.

**Access control**: who may be counted is the `sources` table: sites (by host, with the port if there is one) and GitHub accounts. `allowed.yml` only fills it the first time, when it is empty; after that the list is changed in the dashboard's Sources dialog (`sources.go`, behind the login), and a change applies at once because the list is also held in memory (reloaded every minute). A source is matched whole: it used to be a substring match, which let `https://changkun.de.example.com` pass for `https://changkun.de`. Sources that were turned away are remembered in memory since the service started (at most 200) so that the dialog can offer them. GitHub mode also validates requests from GitHub's camo proxy. With `production: false` in `allowed.yml`, a page on localhost may report.

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

Indexes are defined in `migrations/001_initial.sql`. `migrations/002_sources.sql` adds the `sources` table; the service applies it itself when it starts.

## Deployment

Docker-based with Alpine Linux. Uses `URLSTAT_DB` environment variable for PostgreSQL connection (defaults to `postgres://urlstat:urlstat@urlstatdb:5432/urlstat?sslmode=disable`). Uses `URLSTAT_ADDR` environment variable (defaults to `0.0.0.0:80`). `AUTH_ALLOWED_PRINCIPALS` (comma-separated emails or principal ids), `AUTH_URL` (default `https://auth.latere.ai`) and `AUTH_JWKS_URL` configure who may clean up; copy `.env.template` to `.env` beside `docker-compose.yml`.
