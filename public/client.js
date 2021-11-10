let endpoint = 'https://www.changkun.de/urlstat'
let report = []

const p = document.getElementById('urlstat-page-pv')
const u = document.getElementById('urlstat-page-uv')
if (p !== null || u !== null) {
    report.push('page')
}

const sp = document.getElementById('urlstat-site-pv')
const su = document.getElementById('urlstat-site-uv')
if (sp !== null || su !== null) {
    report.push('site')
}

if (report.length !== 0) {
    endpoint += '?report=' + report.join('+')
}

const h = new Headers({'urlstat-url': window.location.href,'urlstat-ua': navigator.userAgent})
const r = new Request(endpoint, {method: 'GET', headers: h})
fetch(r).then(resp => {
    if (!resp.ok) throw Error(resp.statusText)
    return resp
})
.then(resp => resp.json()).then(resp => {
    if (p !== null) {
        p.textContent = resp.page_pv
    }
    if (u !== null) {
        u.textContent = resp.page_uv
    }
    if (sp !== null) {
        sp.textContent = resp.site_pv
    }
    if (su !== null) {
        su.textContent = resp.site_uv
    }
}).catch(err => console.error(err))
