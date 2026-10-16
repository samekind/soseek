package api

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/samekind/soseek/internal/adminui"
	"github.com/samekind/soseek/internal/core"
	"github.com/samekind/soseek/internal/credentials"
)

const sessionCookie = "soseek_admin_session"

// WithCredentials shares the persistent write-only vault with the provider client.
func (s *Server) WithCredentials(m *credentials.Manager) *Server { s.credentials = m; return s }

func (s *Server) registerAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/admin/", http.StatusSeeOther) })
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/admin/", http.StatusSeeOther) })
	mux.Handle("GET /admin/", adminui.Handler())
	mux.Handle("POST /admin/api/session", s.sameOrigin(http.HandlerFunc(s.login)))
	mux.Handle("GET /admin/api/session", s.admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]any{"principal": principal(r)}) })))
	mux.Handle("DELETE /admin/api/session", s.admin(http.HandlerFunc(s.logout)))
	mux.Handle("GET /admin/api/overview", s.admin(http.HandlerFunc(s.overview)))
	mux.Handle("GET /admin/api/principals", s.admin(http.HandlerFunc(s.adminPrincipals)))
	mux.Handle("POST /admin/api/principals", s.admin(http.HandlerFunc(s.createPrincipal)))
	mux.Handle("PATCH /admin/api/principals/{id}", s.admin(http.HandlerFunc(s.updatePrincipal)))
	mux.Handle("POST /admin/api/principals/{id}/rotate", s.admin(http.HandlerFunc(s.rotatePrincipal)))
	mux.Handle("DELETE /admin/api/principals/{id}", s.admin(http.HandlerFunc(s.revokePrincipal)))
	mux.Handle("GET /admin/api/credentials", s.admin(http.HandlerFunc(s.listCredentials)))
	mux.Handle("PUT /admin/api/credentials/{id}", s.admin(http.HandlerFunc(s.setCredential)))
	mux.Handle("POST /admin/api/credentials/{id}/disable", s.admin(http.HandlerFunc(s.disableCredential)))
	mux.Handle("DELETE /admin/api/credentials/{id}", s.admin(http.HandlerFunc(s.resetCredential)))
	mux.Handle("GET /admin/api/runs", s.admin(http.HandlerFunc(s.adminRuns)))
	mux.Handle("GET /admin/api/runs/{id}", s.admin(http.HandlerFunc(s.adminRun)))
	mux.Handle("POST /admin/api/runs/{id}/cancel", s.admin(http.HandlerFunc(s.adminCancelRun)))
	mux.Handle("GET /admin/api/audit", s.admin(http.HandlerFunc(s.audit)))
}

func loopbackRemote(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	return err == nil && net.ParseIP(host).IsLoopback()
}
func (s *Server) secureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if r.Header.Get("X-Forwarded-Proto") != "https" {
		return false
	}
	if loopbackRemote(r) {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	for _, cidr := range s.cfg.TrustedProxies {
		if _, network, err := net.ParseCIDR(cidr); err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}
func localRequest(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return loopbackRemote(r) && (host == "localhost" || net.ParseIP(host).IsLoopback())
}

// Browsers must send both an exact Origin and a non-simple custom header on every
// mutation, including login. No CORS is enabled. Forwarded scheme is trusted only
// on the loopback hop used by the documented reverse proxy.
func (s *Server) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.secureRequest(r) && !localRequest(r) {
			fail(w, core.E("forbidden", "management requires HTTPS"))
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			origin, err := url.Parse(r.Header.Get("Origin"))
			scheme := "http"
			if s.secureRequest(r) {
				scheme = "https"
			}
			if err != nil || origin.Scheme != scheme || origin.Host != r.Host || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || r.Header.Get("X-Soseek-Admin") != "1" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				fail(w, core.E("forbidden", "same-origin management request required"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) admin(next http.Handler) http.Handler {
	return s.sameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || len(cookie.Value) > 256 {
			fail(w, core.E("unauthorized", "administrator session required"))
			return
		}
		p, err := s.db.AuthenticateSession(r.Context(), cookie.Value)
		if err != nil {
			fail(w, core.E("unauthorized", "administrator session expired or revoked"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	}))
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	s.loginMu.Lock()
	if time.Since(s.loginWindow) >= time.Minute {
		s.loginWindow = time.Now()
		s.loginCount = 0
	}
	s.loginCount++
	over := s.loginCount > 30
	s.loginMu.Unlock()
	if over {
		w.Header().Set("Retry-After", "60")
		fail(w, core.E("rate_limited", "too many login attempts; wait one minute"))
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if err := decode(w, r, &input); err != nil {
		fail(w, err)
		return
	}
	if len(input.Token) > 512 {
		fail(w, core.E("unauthorized", "administrator token required"))
		return
	}
	raw, p, err := s.db.CreateSession(r.Context(), strings.TrimSpace(input.Token))
	if err != nil {
		fail(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: raw, Path: "/admin/api", HttpOnly: true, Secure: s.secureRequest(r), SameSite: http.SameSiteStrictMode, MaxAge: 43200, Expires: time.Now().Add(12 * time.Hour)})
	reply(w, 200, map[string]any{"principal": p, "expires_in": 43200})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(sessionCookie)
	if err := s.db.DeleteSession(r.Context(), cookie.Value); err != nil {
		fail(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/admin/api", HttpOnly: true, Secure: s.secureRequest(r), SameSite: http.SameSiteStrictMode, MaxAge: -1})
	reply(w, 200, map[string]bool{"ok": true})
}
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	data, err := s.db.AdminOverview(r.Context(), principal(r).ID)
	if err != nil {
		fail(w, err)
		return
	}
	channels := make([]map[string]any, 0, len(s.cfg.Channels))
	disabled := map[string]bool{}
	if s.credentials != nil {
		entries, err := s.credentials.List()
		if err != nil {
			fail(w, err)
			return
		}
		for _, entry := range entries {
			for _, channel := range entry.Channels {
				disabled[channel] = entry.Disabled || !entry.Configured
			}
		}
	}
	for _, ch := range s.cfg.Channels {
		channels = append(channels, map[string]any{"id": ch.ID, "provider": ch.Provider, "operations": ch.Operations, "model": ch.Model, "disabled": ch.Disabled || disabled[ch.ID], "quota_group": ch.QuotaGroup})
	}
	reply(w, 200, map[string]any{"version": core.Version, "revision": s.cfg.Revision, "limits": s.cfg.Limits, "statistics": data.Statistics, "usage": data.Usage, "channels": channels, "principal": principal(r)})
}
func (s *Server) adminPrincipals(w http.ResponseWriter, r *http.Request) {
	items, err := s.db.ListAdminPrincipals(r.Context(), principal(r).ID)
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, map[string]any{"items": items})
}

type principalInput struct {
	Name      string   `json:"name"`
	DailyUSD  string   `json:"daily_usd"`
	Providers []string `json:"providers"`
}

func (s *Server) principalInput(w http.ResponseWriter, r *http.Request) (principalInput, int64, error) {
	var in principalInput
	if err := decode(w, r, &in); err != nil {
		return in, 0, err
	}
	in.Name = strings.TrimSpace(in.Name)
	m, err := core.ParseUSD(in.DailyUSD)
	if err != nil || m <= 0 || m > s.cfg.Limits.DailyMicros || len(in.Name) == 0 || len(in.Name) > 100 || strings.ContainsAny(in.Name, "\n\r\x00") {
		return in, 0, core.E("invalid_request", "name (1-100 bytes) and positive daily budget within deployment limit required")
	}
	known := map[string]bool{}
	for _, ch := range s.cfg.Channels {
		known[ch.Provider] = true
	}
	seen := map[string]bool{}
	for _, p := range in.Providers {
		if !known[p] || seen[p] {
			return in, 0, core.E("invalid_request", "providers must be unique configured provider names")
		}
		seen[p] = true
	}
	return in, m, nil
}
func (s *Server) createPrincipal(w http.ResponseWriter, r *http.Request) {
	in, m, err := s.principalInput(w, r)
	if err != nil {
		fail(w, err)
		return
	}
	p, token, err := s.db.AdminCreatePrincipal(r.Context(), principal(r).ID, in.Name, m, in.Providers)
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 201, map[string]any{"principal": p, "token": token})
}
func (s *Server) updatePrincipal(w http.ResponseWriter, r *http.Request) {
	in, m, err := s.principalInput(w, r)
	if err != nil {
		fail(w, err)
		return
	}
	p, err := s.db.AdminUpdatePrincipal(r.Context(), principal(r).ID, r.PathValue("id"), in.Name, m, in.Providers)
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, map[string]any{"principal": p})
}
func (s *Server) rotatePrincipal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Replacing one's own token invalidates every session. Keep that recovery action
	// in the local CLI; the panel always retains an authenticated administrator.
	if id == principal(r).ID {
		fail(w, core.E("conflict", "cannot rotate the current administrator token from this session"))
		return
	}
	items, err := s.db.ListAdminPrincipals(r.Context(), principal(r).ID)
	if err != nil {
		fail(w, err)
		return
	}
	for _, p := range items {
		if p.ID == id {
			token, err := s.db.AdminRotateToken(r.Context(), principal(r).ID, id)
			if err != nil {
				fail(w, err)
				return
			}
			reply(w, 200, map[string]any{"principal": p, "token": token})
			return
		}
	}
	fail(w, core.E("not_found", "principal not found"))
}
func (s *Server) revokePrincipal(w http.ResponseWriter, r *http.Request) {
	if err := s.db.AdminRevokePrincipal(r.Context(), principal(r).ID, r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}
func (s *Server) listCredentials(w http.ResponseWriter, r *http.Request) {
	if s.credentials == nil {
		fail(w, core.E("planner_unavailable", "credential management unavailable"))
		return
	}
	items, err := s.credentials.List()
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, map[string]any{"items": items})
}
func (s *Server) changeCredential(w http.ResponseWriter, r *http.Request, action string, change func(string) error) {
	if s.credentials == nil {
		fail(w, core.E("planner_unavailable", "credential management unavailable"))
		return
	}
	id := r.PathValue("id")
	// Record intent before changing a file: a crash can leave a requested event,
	// never an unaccounted successful mutation. Neither event includes key data.
	if err := s.db.RecordAudit(r.Context(), principal(r).ID, action+"_requested", id); err != nil {
		fail(w, err)
		return
	}
	if err := change(id); err != nil {
		fail(w, err)
		return
	}
	if err := s.db.RecordAudit(r.Context(), principal(r).ID, action, id); err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}
func (s *Server) setCredential(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Key string `json:"key"`
	}
	if err := decode(w, r, &input); err != nil {
		fail(w, err)
		return
	}
	s.changeCredential(w, r, "credential.update", func(id string) error { return s.credentials.Set(id, input.Key) })
}
func (s *Server) disableCredential(w http.ResponseWriter, r *http.Request) {
	s.changeCredential(w, r, "credential.disable", func(id string) error { return s.credentials.Disable(id) })
}
func (s *Server) resetCredential(w http.ResponseWriter, r *http.Request) {
	s.changeCredential(w, r, "credential.reset", func(id string) error { return s.credentials.Reset(id) })
}
func pageLimit(r *http.Request) (int, error) {
	limit := 30
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return 0, core.E("invalid_request", "limit must be between 1 and 100")
		}
		limit = n
	}
	return limit, nil
}
func (s *Server) adminRuns(w http.ResponseWriter, r *http.Request) {
	limit, err := pageLimit(r)
	if err != nil {
		fail(w, err)
		return
	}
	data, err := s.db.ListAdminRuns(r.Context(), principal(r).ID, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, data)
}
func (s *Server) adminRun(w http.ResponseWriter, r *http.Request) {
	owner, err := s.db.RunOwner(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	run, err := s.db.GetRun(r.Context(), owner, r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, run)
}
func (s *Server) adminCancelRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	owner, err := s.db.RunOwner(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if err = s.db.RecordAudit(r.Context(), principal(r).ID, "run.cancel_requested", id); err != nil {
		fail(w, err)
		return
	}
	if err := s.db.Cancel(r.Context(), owner, id); err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}
func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	limit, err := pageLimit(r)
	if err != nil {
		fail(w, err)
		return
	}
	data, err := s.db.ListAudit(r.Context(), principal(r).ID, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, data)
}
