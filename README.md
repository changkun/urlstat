<p align="center">
  <a href="https://changkun.de/urlstat/dashboard"><img src=".github/readme/dashboard.png" alt="The urlstat dashboard: totals, a daily chart of views and visitors, sections and pages" width="860"></a>
</p>

<p align="center">
  <a href="https://changkun.de/urlstat/dashboard"><img alt="Live dashboard" src="https://img.shields.io/badge/live-dashboard-2a78d6"></a>
  <a href="https://github.com/changkun/urlstat/actions/workflows/test.yml"><img alt="test" src="https://github.com/changkun/urlstat/actions/workflows/test.yml/badge.svg"></a>
  <img alt="Go version" src="https://img.shields.io/github/go-mod/go-version/changkun/urlstat">
  <a href="LICENSE"><img alt="License: MIT" src="https://img.shields.io/github/license/changkun/urlstat"></a>
</p>

# urlstat

urlstat counts page views and visitors. A page loads one script, or a
repository shows one badge, and the visits go into a single Postgres table
that a dashboard reads. It is one Go binary with no external scripts on its
pages.

It runs [changkun.de](https://changkun.de),
[golang.design](https://golang.design) and a few friends' sites, and its
dashboard is public: **[changkun.de/urlstat/dashboard](https://changkun.de/urlstat/dashboard)**.

## What it does

- **Counts** views and visitors for every page of every allowed site, and
  can show a page's own numbers on the page.
- **Badges** for GitHub repositories, counting how often a README is seen.
- **A dashboard** with totals against the period before, a daily chart you
  can drag across to zoom in, and the pages grouped by path so that a section
  such as `/blog` has totals of its own.
- **Where visits came from**: the site that linked to the page, or a tag in
  the link.
- **What visitors used**: device, system and browser, with crawlers counted
  apart from people, and how many visitors came back on another day.
- **Signed in**: the latest visitors one by one, cleaning up what crawlers
  and scrapers left behind, and the list of sites that may be counted.

<table>
  <tr>
    <td width="50%"><img src=".github/readme/zoom.png" alt="Dragging across the chart to select a range of days"><br><sub>Drag across the chart and everything below shows only those days.</sub></td>
    <td width="50%"><img src=".github/readme/dashboard-dark.png" alt="The dashboard in its dark theme, over one year"><br><sub>Light or dark, after the system. Hover or use the arrow keys to read a day.</sub></td>
  </tr>
  <tr>
    <td><img src=".github/readme/came-from.png" alt="A list of where visits came from: linkedin.com, direct, google.com, github.com and others"><br><sub>Where visits came from, for the period and section in view.</sub></td>
    <td><img src=".github/readme/used.png" alt="What visitors used, by device, with crawlers counted apart"><br><sub>What people used, and how much of the traffic was crawlers.</sub></td>
  </tr>
  <tr>
    <td><img src=".github/readme/visitor.png" alt="One visitor: the pages it read in order, and where it came from"><br><sub>Signed in: what one visitor read, with a way to remove a scraper's visits.</sub></td>
    <td><img src=".github/readme/cleanup.png" alt="A dialog saying how many visits of how many pages would be deleted, asking to confirm"><br><sub>Cleaning up says what would be deleted before anything is.</sub></td>
  </tr>
</table>

## Count a page

Add the script to the page:

```html
<script async src="https://changkun.de/urlstat/client.js"></script>
```

To show the numbers, give any of these ids to an element; the script fills
in the ones it finds:

```html
<span id="urlstat-page-pv"></span>  <!-- views of this page -->
<span id="urlstat-page-uv"></span>  <!-- visitors of this page -->
<span id="urlstat-site-pv"></span>  <!-- views of the whole site -->
<span id="urlstat-site-uv"></span>  <!-- visitors of the whole site -->
```

A site has to be allowed before its visits are counted. On changkun.de that
is by request: send an email to hi@changkun.de. On your own installation it
is one click, see [Sources](#signed-in).

Apps often hide where a link was opened from, so a link you share is best
tagged, for instance `https://example.com/post?utm_source=linkedin`. The
visit then counts as coming from LinkedIn, and the page is counted under its
plain address either way.

## Count a repository

Put the badge in the README, with the repository's own name:

```markdown
![](https://changkun.de/urlstat?mode=github&repo=changkun/urlstat)
```

![](https://changkun.de/urlstat?mode=github&repo=changkun/urlstat)

The account has to be allowed, like a site.

## The dashboard

`/urlstat/dashboard` is public and shows counts only.

- **Host and period.** Any tracked host, over the last 7, 30 or 90 days or a
  year, each compared with the period before. The address keeps the host,
  period, range and path, so a view can be bookmarked or shared.
- **Zoom.** Drag across the chart to show only those days; double-click a day
  to open it alone.
- **Sections and pages.** Pages are grouped by the next part of their path.
  Opening `/blog`, then `/blog/posts`, narrows the totals, the chart and
  every list to the pages below it.
- **Came from.** The referring site under its main name (`google.de` and
  `google.co.jp` are both `google.com`), a `utm_source` tag when the link
  has one, or "Direct" when the browser names nothing. Visits from another
  page of the same site are counted apart.
- **Used.** Device, system and browser of people, with crawlers set apart,
  and the share of visitors seen on more than one day.

### Signed in

An account named in `AUTH_ALLOWED_PRINCIPALS` gets three more things.

<table>
  <tr>
    <td width="50%"><img src=".github/readme/visitors.png" alt="A table of the latest visitors with what each used and where it came from"><br><sub>The latest visitors.</sub></td>
    <td width="50%"><img src=".github/readme/sources.png" alt="The end of the Sources dialog: two sites seen but not allowed, each with an Allow button"><br><sub>Sites that tried and were turned away, to allow with one click.</sub></td>
  </tr>
</table>

- **Visitors.** The latest addresses of the period: what each used, where it
  came from, how much it read, and, opened, every page in order. An address
  with what it read is personal data, which is why this is not public.
- **Clean up.** Tick pages, or name a threshold ("every page with fewer than
  10 visits ever"), or open a visitor that turned out to be a machine. The
  dashboard says how many visits of which pages would go, and deletes only
  when that is confirmed. Deleting is for all time and cannot be undone.
- **Sources.** The sites and GitHub accounts that may be counted. Adding or
  removing one applies at once, and removing keeps the visits already there.
  Sites that loaded the script without being allowed are listed with their
  attempts, to be allowed with one click.

## What is stored

One row per visit: the host and path of the page, the time, the visitor's IP
address and browser string, and where the visit came from. Visitors are told
apart by IP address and nothing else, so two people behind one address are
one visitor. Crawlers that run scripts are recorded too; the dashboard sets
them apart by their browser string.

## Run your own

It needs Go 1.27, Postgres, and Docker if you want the container.

```bash
# The table. Later schema changes are applied by the service when it starts.
psql "$URLSTAT_DB" -f migrations/001_initial.sql

make build   # the Linux binary and the urlstat:latest image
make up      # docker compose up -d
```

| Setting | Default | |
|---|---|---|
| `URLSTAT_DB` | `postgres://urlstat:urlstat@urlstatdb:5432/urlstat?sslmode=disable` | The database |
| `URLSTAT_ADDR` | `0.0.0.0:80` | Where to listen |
| `AUTH_ALLOWED_PRINCIPALS` | none | Emails or account ids that may sign in to manage; without it nobody can |
| `AUTH_URL` | `https://auth.latere.ai` | The issuer of the sign-in tokens |
| `AUTH_JWKS_URL` | `$AUTH_URL/.well-known/jwks.json` | The keys those tokens are verified with |

Four things are particular to the installation on changkun.de, and yours
will want them changed:

- `public/client.js` reports to `https://www.changkun.de/urlstat`. Change its
  first line to your address.
- `docker-compose.yml` joins two networks of the author's server, one for
  the reverse proxy and one for the database. Point it at your own.
- `allowed.yml` is the list of sites and accounts a new installation starts
  with. It is read once, into the database; after that the list is managed
  in the dashboard.
- Signing in uses the author's login service. The server accepts any issuer
  of RS256 tokens that publishes its keys (`AUTH_URL`, `AUTH_JWKS_URL`) and
  whose tokens carry an `email` or `sub`; the dashboard expects a script at
  `/login-sdk.js` on its own origin that provides the token. Without one the
  dashboard still works, read-only.

## Development

```bash
make all     # build the binary
URLSTAT_DB='postgres://urlstat:urlstat@localhost:5432/urlstat?sslmode=disable' go test ./...
```

The tests read and write a real database, at the address above, with
`migrations/001_initial.sql` applied. They use hosts of their own
(`*.invalid`) and remove what they insert. [`AGENTS.md`](AGENTS.md) describes
how the pieces fit: the queries, the caches, and why visitors are counted by
grouping twice.

## License

MIT &copy; 2021 [Changkun Ou](https://changkun.de)
