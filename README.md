# urlstat

`urlstat` provides basic facility for pv/uv statistic cross websites.
It is designed for [blog.changkun.de](https://blog.changkun.de),
[golang.design/research](https://golang.design/research) and etc.

## Usage

Add the following script to a page:

```html
<script async src="//changkun.de/urlstat/client.js"></script>
```

The script will look for elements with ID `urlstat-page-pv`
and `urlstat-page-uv` and manipulate the information
if the retrieve succeed. For instance:

```html
<span id="urlstat-page-pv"><!-- info will be inserted --></span>
<span id="urlstat-page-uv"><!-- info will be inserted --></span>
```

An example, see https://golang.design/research/zero-alloc-call-sched/

![image](https://user-images.githubusercontent.com/5498964/107117728-9cc01700-687c-11eb-92a3-495a4672717a.png)

## License

MIT &copy; 2021 [Changkun Ou](https://changkun.de)