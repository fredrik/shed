// Package httpgate is the HTTP front door: <vm>.shed.localhost routes to a
// port inside that VM, exe.dev-style. VMs are private by default — the
// gate wants a signed token (from `ssh shed share <vm>`) which it then
// pins as a cookie; `share set-public` opens a VM up.
package httpgate

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"html"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"github.com/fredrik/shed/internal/httpgate/webui"
	"github.com/fredrik/shed/internal/vm"
	"github.com/fredrik/shed/internal/vm/vmspec"
)

// DefaultPort is where the proxy points when the image exposes nothing
// (exe.dev's convention).
const DefaultPort = 8000

type Server struct {
	Addr   string
	Suffix string // e.g. "shed.localhost"
	Mgr    *vm.Manager
	secret []byte

	srv *http.Server
}

// assets serves the gate's favicon and preview image (see webui).
var assets = webui.Handler()

// EnsureSecret loads or creates the HMAC key used to sign share tokens.
func (s *Server) EnsureSecret(path string) error {
	data, err := os.ReadFile(path)
	if err == nil && len(data) >= 32 {
		s.secret = data
		return nil
	}
	s.secret = make([]byte, 32)
	if _, err := rand.Read(s.secret); err != nil {
		return err
	}
	return os.WriteFile(path, s.secret, 0o600)
}

// Token returns the share token for a VM name.
func (s *Server) Token(name string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("shed-share:" + name))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

func (s *Server) validToken(name, token string) bool {
	want := s.Token(name)
	return subtle.ConstantTimeCompare([]byte(want), []byte(token)) == 1
}

func (s *Server) ListenAndServe() error {
	s.srv = &http.Server{Addr: s.Addr, Handler: http.HandlerFunc(s.handle)}
	log.Printf("httpgate: listening on %s (http://<vm>.%s)", s.Addr, s.hostWithPort(s.Suffix))
	return s.srv.ListenAndServe()
}

func (s *Server) Close() error {
	if s.srv != nil {
		return s.srv.Close()
	}
	return nil
}

func (s *Server) hostWithPort(host string) string {
	_, port, err := net.SplitHostPort(s.Addr)
	if err != nil || port == "80" {
		return host
	}
	return host + ":" + port
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	// Our own favicon and preview image, on every host: an unfurler that
	// hits a private vm's page needs the og:image without a token.
	if strings.HasPrefix(r.URL.Path, webui.Prefix) {
		assets.ServeHTTP(w, r)
		return
	}

	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	if host == s.Suffix || !strings.HasSuffix(host, "."+s.Suffix) {
		s.landing(w, r)
		return
	}
	name := strings.TrimSuffix(host, "."+s.Suffix)

	rec, ok := s.Mgr.Get(name)
	if !ok {
		s.page(w, r, http.StatusNotFound, "no such vm",
			fmt.Sprintf("There is no vm named %q.", name),
			fmt.Sprintf("Create it: <code>ssh shed new %s</code>", html.EscapeString(name)))
		return
	}

	if !s.authorized(w, r, rec) {
		return
	}

	if rec.State != vmspec.StateRunning {
		s.page(w, r, http.StatusBadGateway, "vm is "+string(rec.State),
			fmt.Sprintf("vm %q is %s.", name, rec.State),
			fmt.Sprintf("Start it: <code>ssh shed start %s</code>", html.EscapeString(name)))
		return
	}
	run, ok := s.Mgr.Running(name)
	if !ok {
		s.page(w, r, http.StatusBadGateway, "vm not reachable", "The vm stopped just now.", "")
		return
	}

	port := TargetPort(rec)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = fmt.Sprintf("guest:%d", port)
			pr.Out.Host = r.Host
			pr.SetXForwarded()
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return run.DialGuest(ctx, port)
			},
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.page(w, r, http.StatusBadGateway, "nothing listening",
				fmt.Sprintf("vm %q is running but nothing answered on port %d.", name, port),
				fmt.Sprintf("Change the port: <code>ssh shed share port %s &lt;port&gt;</code>", html.EscapeString(name)))
		},
	}
	proxy.ServeHTTP(w, r)
}

// authorized enforces private-by-default. It accepts: public VMs, a valid
// shed_token query parameter (then pins a cookie and redirects to strip the
// token), or the pinned cookie.
func (s *Server) authorized(w http.ResponseWriter, r *http.Request, rec vmspec.VM) bool {
	if rec.Share.Public {
		return true
	}
	name := rec.Spec.Name
	cookieName := "shed_auth_" + name

	if token := r.URL.Query().Get("shed_token"); token != "" {
		if s.validToken(name, token) {
			http.SetCookie(w, &http.Cookie{
				Name: cookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
			})
			q := r.URL.Query()
			q.Del("shed_token")
			clean := *r.URL
			clean.RawQuery = q.Encode()
			http.Redirect(w, r, clean.String(), http.StatusFound)
			return false
		}
	}
	if c, err := r.Cookie(cookieName); err == nil && s.validToken(name, c.Value) {
		return true
	}
	s.page(w, r, http.StatusForbidden, "private vm",
		fmt.Sprintf("vm %q is private.", name),
		fmt.Sprintf("Get a link: <code>ssh shed share %s</code> — or make it public: <code>ssh shed share set-public %s</code>", html.EscapeString(name), html.EscapeString(name)))
	return false
}

func (s *Server) landing(w http.ResponseWriter, r *http.Request) {
	s.page(w, r, http.StatusOK, "shed",
		"Linux microVMs on your Mac, over ssh. This is the shed HTTP front door.",
		"Each vm is reachable at <code>http://&lt;name&gt;."+s.hostWithPort(s.Suffix)+"</code>. List yours: <code>ssh shed ls</code>")
}

// TargetPort picks the forwarded port: share override, else smallest
// exposed TCP port, else the exe.dev default.
func TargetPort(rec vmspec.VM) int {
	if rec.Share.Port > 0 {
		return rec.Share.Port
	}
	if len(rec.Image.ExposedPorts) > 0 {
		return rec.Image.ExposedPorts[0]
	}
	return DefaultPort
}

// page renders one of the gate's own pages; lead is plain text, hint may
// carry <code> markup (escape anything user-controlled you put in it).
func (s *Server) page(w http.ResponseWriter, r *http.Request, code int, title, lead, hint string) {
	base := webui.BaseURL(r)
	webui.Render(w, code, webui.Page{
		Title: title,
		Lead:  lead,
		Hint:  template.HTML(hint),
		Base:  base,
		URL:   base + r.URL.EscapedPath(),
	})
}

// URLWithToken builds the tokened share URL for a VM.
func (s *Server) URLWithToken(name string) string {
	return fmt.Sprintf("http://%s?shed_token=%s", s.hostWithPort(name+"."+s.Suffix), url.QueryEscape(s.Token(name)))
}

// URL is the plain URL for a VM.
func (s *Server) URL(name string) string {
	return fmt.Sprintf("http://%s", s.hostWithPort(name+"."+s.Suffix))
}
