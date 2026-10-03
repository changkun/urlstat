# urlstat

`urlstat` provides basic facility for pv/uv statistic cross websites.
It is designed for [blog.changkun.de](https://blog.changkun.de),
[golang.design/research](https://golang.design/research) and etc.

## Usage

### Plain Mode

Add the following script to a page:

```html
<script async src="//changkun.de/urlstat/client.js"></script>
```

The script will look for elements with ID `urlstat-site-pv`, `urlstat-site-uv`, `urlstat-page-pv`, and `urlstat-page-uv` and manipulate the information
if the retrieve succeed. For instance:

```html
<span id="urlstat-site-pv"><!-- info will be inserted --></span>
<span id="urlstat-site-uv"><!-- info will be inserted --></span>
<span id="urlstat-page-pv"><!-- info will be inserted --></span>
<span id="urlstat-page-uv"><!-- info will be inserted --></span>
```

An example, see https://golang.design/research/zero-alloc-call-sched/

![image](https://user-images.githubusercontent.com/5498964/107117728-9cc01700-687c-11eb-92a3-495a4672717a.png)


### GitHub Mode

Use query parameter: `mode=github` and `repo=username/reponame`. For instance:

```
![](https://changkun.de/urlstat?mode=github&repo=changkun/urlstat)
```

![](https://changkun.de/urlstat?mode=github&repo=changkun/urlstat)

## Dashboard

`/urlstat/dashboard` shows the page views and visitors of every tracked host:
totals for the last 7 days to a year against the period before, a daily
chart, and the pages, which can be opened section by section (`/blog`,
`/blog/posts`, ...) to total everything under one path. The page's address
keeps the host, period and path, so a view can be bookmarked. Dragging across
the chart zooms in on those days.

**Came from** says where visits came from: the site that linked to the page,
or a tag in the link. Apps often hide where a link was opened from, so a
link you share is best tagged, for instance
`https://example.com/post?utm_source=linkedin`; the page is counted under
its plain address either way.

Signed in with the changkun.de login, an allowed account can also clean up:
tick pages, or name a threshold ("every page with fewer than 10 visits"),
read what would be deleted, and confirm. `AUTH_ALLOWED_PRINCIPALS` names the
accounts; see `.env.template`.

The same account manages who is counted, under **Sources**: the sites that
load the script and the GitHub accounts that show the badge. Adding or
removing one applies at once. Sites and accounts that tried and were turned
away are listed there too, and can be allowed with one click. `allowed.yml`
only provides the list a new installation starts with.

## License

MIT &copy; 2021 [Changkun Ou](https://changkun.de)