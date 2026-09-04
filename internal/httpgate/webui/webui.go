// Package webui holds the httpgate's own look: the embedded favicon and
// preview image, and the template for the pages the gate serves itself
// (landing, private vm, no such vm, ...). Colours are Rekorderlig's
// flavour palette — strawberry, passionfruit, lime, wild berries, peach —
// on a cream label.
//
// Assets live under Prefix on every host the gate answers for, so a
// link-unfurler (Slack, iMessage, ...) that lands on one of our pages can
// fetch the og:image without a share token. Sources: favicon.svg is hand
// drawn; og.png and apple-touch-icon.png are headless-Chromium renders of
// og.svg and favicon.svg (1200x630 and 180x180) — re-render if you touch
// the SVGs.
package webui

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
)

// Prefix is the reserved path under which the gate serves its own assets.
// Requests here never reach a VM.
const Prefix = "/_shed/"

//go:embed assets
var assets embed.FS

// Handler serves the embedded assets at Prefix.
func Handler() http.Handler {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	files := http.StripPrefix(Prefix, http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r) // no directory listings
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		files.ServeHTTP(w, r)
	})
}

// Page is one of the gate's own pages.
type Page struct {
	Title string        // short, e.g. "private vm"; the tab reads "<Title> · shed"
	Lead  string        // one plain-text sentence; doubles as the preview description
	Hint  template.HTML // what to do about it; may contain <code>
	Base  string        // scheme://host the page was requested on, no trailing slash
	URL   string        // canonical URL of the page, without query string
}

// BaseURL returns scheme://host for the request, honouring X-Forwarded-Proto
// so previews point back through whatever fronted us.
func BaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p == "http" || p == "https" {
		scheme = p
	}
	return scheme + "://" + r.Host
}

// Render writes the page. Query strings are deliberately absent from the
// canonical URL so a share token never ends up in a preview.
func Render(w http.ResponseWriter, code int, p Page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = pageTmpl.Execute(w, p)
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · shed</title>
<meta name="description" content="{{.Lead}}">
<meta name="theme-color" content="#E9536F">
<link rel="icon" type="image/svg+xml" href="/_shed/favicon.svg">
<link rel="apple-touch-icon" href="/_shed/apple-touch-icon.png">
<meta property="og:type" content="website">
<meta property="og:site_name" content="shed">
<meta property="og:title" content="{{.Title}} · shed">
<meta property="og:description" content="{{.Lead}}">
<meta property="og:url" content="{{.URL}}">
<meta property="og:image" content="{{.Base}}/_shed/og.png">
<meta property="og:image:type" content="image/png">
<meta property="og:image:width" content="1200">
<meta property="og:image:height" content="630">
<meta property="og:image:alt" content="shed — Linux microVMs on your Mac, over ssh.">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:title" content="{{.Title}} · shed">
<meta name="twitter:description" content="{{.Lead}}">
<meta name="twitter:image" content="{{.Base}}/_shed/og.png">
<style>
body{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;background:#241E2C;color:#FFF6EA;display:grid;place-items:center;min-height:100vh;margin:0}
main{max-width:34rem;padding:2rem}
h1{font-size:1.2rem;margin:0 0 .75rem;display:flex;align-items:center;gap:.6rem}
h1 img{width:1.6rem;height:1.6rem}
p{line-height:1.5;margin:.4rem 0;color:#B9AFC2}
code{background:#332A3E;color:#A9D141;padding:.15rem .4rem;border-radius:4px}
</style></head>
<body><main><h1><img src="/_shed/favicon.svg" alt="">{{.Title}}</h1><p>{{.Lead}}</p><p>{{.Hint}}</p></main></body></html>
`))
