package webui

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAssets(t *testing.T) {
	h := Handler()
	cases := []struct {
		path, ctype string
		code        int
	}{
		{"/_shed/favicon.svg", "image/svg+xml", 200},
		{"/_shed/og.png", "image/png", 200},
		{"/_shed/apple-touch-icon.png", "image/png", 200},
		{"/_shed/", "", 404},
		{"/_shed/nope.txt", "", 404},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", c.path, nil))
		if rec.Code != c.code {
			t.Errorf("%s: code %d, want %d", c.path, rec.Code, c.code)
		}
		if c.ctype != "" && !strings.HasPrefix(rec.Header().Get("Content-Type"), c.ctype) {
			t.Errorf("%s: content-type %q, want %s", c.path, rec.Header().Get("Content-Type"), c.ctype)
		}
		if c.code == 200 && rec.Body.Len() < 500 {
			t.Errorf("%s: suspiciously small (%d bytes)", c.path, rec.Body.Len())
		}
	}
}

func TestRender(t *testing.T) {
	rec := httptest.NewRecorder()
	Render(rec, http.StatusForbidden, Page{
		Title: "private vm",
		Lead:  `vm "box" is private & <locked>.`,
		Hint:  template.HTML(`Get a link: <code>ssh shed share box</code>`),
		Base:  "http://box.shed.localhost:8080",
		URL:   "http://box.shed.localhost:8080/",
	})
	if rec.Code != 403 {
		t.Fatalf("code %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<title>private vm · shed</title>`,
		`<meta property="og:image" content="http://box.shed.localhost:8080/_shed/og.png">`,
		`<meta property="og:url" content="http://box.shed.localhost:8080/">`,
		`<meta property="og:description" content="vm &#34;box&#34; is private &amp; &lt;locked&gt;.">`,
		`<meta name="twitter:card" content="summary_large_image">`,
		`<link rel="icon" type="image/svg+xml" href="/_shed/favicon.svg">`,
		`<code>ssh shed share box</code>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<locked>") {
		t.Error("lead was not escaped")
	}
}

func TestBaseURL(t *testing.T) {
	r := httptest.NewRequest("GET", "http://box.shed.localhost:8080/x?shed_token=abc", nil)
	if got := BaseURL(r); got != "http://box.shed.localhost:8080" {
		t.Errorf("BaseURL = %q", got)
	}
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := BaseURL(r); got != "https://box.shed.localhost:8080" {
		t.Errorf("forwarded BaseURL = %q", got)
	}
	r.Header.Set("X-Forwarded-Proto", "gopher")
	if got := BaseURL(r); got != "http://box.shed.localhost:8080" {
		t.Errorf("bogus forwarded proto BaseURL = %q", got)
	}
}
