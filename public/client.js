const h = new Headers({'urlstat-url': window.location.href,'urlstat-ua': navigator.userAgent})
const r = new Request('https://www.changkun.de/urlstat', {method: 'GET', headers: h})
fetch(r).then(resp => {
    if (!resp.ok) throw Error(resp.statusText)
    return resp
})
.then(resp => resp.json()).then(resp => {
    const p = document.getElementById('urlstat-page-pv')
    const u = document.getElementById('urlstat-page-uv')
    if (p !== null) {
        p.textContent = resp.page_pv
    }
    if (u !== null) {
        u.textContent = resp.page_uv
    }
}).catch(err => console.error(err))
