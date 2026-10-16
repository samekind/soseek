package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samekind/soseek/internal/config"
	"github.com/samekind/soseek/internal/core"
	"github.com/samekind/soseek/internal/credentials"
	"github.com/samekind/soseek/internal/engine"
	"github.com/samekind/soseek/internal/store"
)

func TestAdminSessionIsolationCSRFAndCredentialManagement(t *testing.T) {
	ctx := context.Background()
	cfg := core.Config{Database: filepath.Join(t.TempDir(), "private", "soseek.db"), Revision: "admin-test", Channels: []core.Channel{{ID: "exa-test", Provider: "exa", BaseURL: "https://example.org", KeyEnv: "SOSEEK_ADMIN_TEST_KEY", Operations: []string{"search"}, CostBounds: map[string]int64{"search": 50000}}}}
	config.Defaults(&cfg)
	t.Setenv("SOSEEK_ADMIN_TEST_KEY", "fixture-original-not-a-real-key")
	db, err := store.Open(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, ownerToken, err := db.CreatePrincipal(ctx, "owner", 5000000, nil)
	if err != nil {
		t.Fatal(err)
	}
	caller, callerToken, err := db.CreatePrincipal(ctx, "caller", 5000000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.GrantAdmin(ctx, owner.ID); err != nil {
		t.Fatal(err)
	}
	vault, err := credentials.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	s := New(cfg, db, engine.New(cfg, db, &testUpstream{})).WithCredentials(vault)
	h := httptest.NewServer(s.Handler())
	defer h.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	request := func(method, path, body string, csrf bool) (int, []byte) {
		t.Helper()
		r, _ := http.NewRequest(method, h.URL+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if csrf {
			r.Header.Set("Origin", h.URL)
			r.Header.Set("X-Soseek-Admin", "1")
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		b, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, b
	}
	for _, path := range []string{"overview", "principals", "credentials", "runs", "audit", "session"} {
		if code, _ := request("GET", "/admin/api/"+path, "", false); code != 401 {
			t.Fatalf("guest %s: %d", path, code)
		}
	}
	loginBody := func(token string) string { b, _ := json.Marshal(map[string]string{"token": token}); return string(b) }
	if code, _ := request("POST", "/admin/api/session", loginBody(ownerToken), false); code != 403 {
		t.Fatalf("login missing CSRF: %d", code)
	}
	if code, _ := request("POST", "/admin/api/session", loginBody(callerToken), true); code != 403 {
		t.Fatalf("caller login: %d", code)
	}
	if code, _ := request("POST", "/admin/api/session", loginBody(ownerToken), true); code != 200 {
		t.Fatalf("owner login: %d", code)
	}
	if code, b := request("GET", "/admin/api/overview", "", false); code != 200 || !strings.Contains(string(b), "reported_micros") {
		t.Fatalf("overview: %d", code)
	}
	if code, _ := request("POST", "/admin/api/principals", `{"name":"blocked","daily_usd":"1","providers":[]}`, false); code != 403 {
		t.Fatalf("CSRF mutation: %d", code)
	}
	code, b := request("POST", "/admin/api/principals", `{"name":"panel-test","daily_usd":"1","providers":["exa"]}`, true)
	if code != 201 {
		t.Fatalf("create: %d %s", code, b)
	}
	var created struct {
		Principal core.Principal `json:"principal"`
		Token     string         `json:"token"`
	}
	if err = json.Unmarshal(b, &created); err != nil {
		t.Fatal(err)
	}
	if created.Token == "" {
		t.Fatal("missing one-time token")
	}
	if code, b = request("GET", "/admin/api/principals", "", false); code != 200 || strings.Contains(string(b), created.Token) || strings.Contains(string(b), "token_hash") {
		t.Fatal("principal list leaks or fails")
	}
	code, b = request("POST", "/admin/api/principals/"+created.Principal.ID+"/rotate", `{}`, true)
	if code != 200 {
		t.Fatalf("rotate: %d", code)
	}
	if _, err = db.Authenticate(ctx, created.Token); err == nil {
		t.Fatal("old token still valid")
	}
	if code, _ = request("PATCH", "/admin/api/principals/"+created.Principal.ID, `{"name":"changed","daily_usd":"0.5","providers":[]}`, true); code != 200 {
		t.Fatalf("edit: %d", code)
	}
	for _, action := range []struct{ method, path string }{{"DELETE", "/admin/api/principals/" + owner.ID}, {"POST", "/admin/api/principals/" + owner.ID + "/rotate"}} {
		if code, _ = request(action.method, action.path, `{}`, true); code != 409 {
			t.Fatalf("self lockout: %d", code)
		}
	}
	if code, _ = request("DELETE", "/admin/api/principals/"+created.Principal.ID, "", true); code != 200 {
		t.Fatalf("revoke: %d", code)
	}
	items, err := vault.List()
	if err != nil || len(items) != 1 {
		t.Fatalf("vault metadata: %v", err)
	}
	credPath := "/admin/api/credentials/" + items[0].ID
	const replacement = "fixture-replacement-not-a-real-key"
	if code, _ = request("PUT", credPath, `{"key":"`+replacement+`"}`, true); code != 200 {
		t.Fatalf("set key: %d", code)
	}
	if key, err := vault.ReadKey(cfg.Channels[0]); err != nil || key != replacement {
		t.Fatal("updated key not effective")
	}
	if code, b = request("GET", "/admin/api/credentials", "", false); code != 200 || strings.Contains(string(b), replacement) || strings.Contains(string(b), "SOSEEK_ADMIN_TEST_KEY") {
		t.Fatal("metadata leaks or fails")
	}
	if code, _ = request("POST", credPath+"/disable", `{}`, true); code != 200 {
		t.Fatalf("disable: %d", code)
	}
	if _, err := vault.ReadKey(cfg.Channels[0]); err == nil {
		t.Fatal("disabled override fell back to original")
	}
	if code, _ = request("DELETE", credPath, "", true); code != 200 {
		t.Fatalf("reset: %d", code)
	}
	if key, err := vault.ReadKey(cfg.Channels[0]); err != nil || key != "fixture-original-not-a-real-key" {
		t.Fatal("reset did not restore original reference")
	}
	req := core.Request{Operation: "search", Query: "caller private query"}
	plan := core.Plan{BudgetUSD: "0.1", Request: core.Request{DeadlineSeconds: 60}}
	run, _, err := db.Submit(ctx, caller, "admin-fixture", req, plan, cfg.Limits.DailyMicros, cfg.Limits.Queue)
	if err != nil {
		t.Fatal(err)
	}
	if code, b = request("GET", "/admin/api/runs", "", false); code != 200 || !strings.Contains(string(b), run.ID) {
		t.Fatalf("run list: %d", code)
	}
	if code, _ = request("GET", "/admin/api/runs/"+run.ID, "", false); code != 200 {
		t.Fatalf("admin cross-owner read: %d", code)
	}
	// The browser cookie must never authorize the existing caller API.
	if code, _ = request("GET", "/v1/runs/"+run.ID, "", false); code != 401 {
		t.Fatalf("cookie authorized caller API: %d", code)
	}
	if code, _ = request("POST", "/admin/api/runs/"+run.ID+"/cancel", `{}`, true); code != 200 {
		t.Fatalf("cancel: %d", code)
	}
	if code, b = request("GET", "/admin/api/audit", "", false); code != 200 || strings.Contains(string(b), replacement) || strings.Contains(string(b), created.Token) {
		t.Fatal("audit leaks or fails")
	}
	if code, _ = request("DELETE", "/admin/api/session", "", true); code != 200 {
		t.Fatalf("logout: %d", code)
	}
	if code, _ = request("GET", "/admin/api/overview", "", false); code != 401 {
		t.Fatalf("logout retained session: %d", code)
	}
}

func TestAdminOriginAndProxyTrust(t *testing.T) {
	s := &Server{}
	handler := s.sameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, tc := range []struct {
		name, url, remote, proto, origin, header string
		code                                     int
	}{
		{"TLS proxy", "http://soseek.example/admin/api/session", "127.0.0.1:20", "https", "https://soseek.example", "1", 204},
		{"forged proxy", "http://soseek.example/admin/api/session", "192.0.2.2:20", "https", "https://soseek.example", "1", 403},
		{"cross origin", "http://soseek.example/admin/api/session", "127.0.0.1:20", "https", "https://evil.example", "1", 403},
		{"missing custom header", "http://soseek.example/admin/api/session", "127.0.0.1:20", "https", "https://soseek.example", "", 403},
		{"null origin", "http://soseek.example/admin/api/session", "127.0.0.1:20", "https", "null", "1", 403},
		{"loopback development", "http://127.0.0.1:18571/admin/api/session", "127.0.0.1:20", "", "http://127.0.0.1:18571", "1", 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", tc.url, nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("X-Forwarded-Proto", tc.proto)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("X-Soseek-Admin", tc.header)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("want %d got %d", tc.code, w.Code)
			}
		})
	}
}

func TestAdminExplicitProxyAllowlist(t *testing.T) {
	s := &Server{cfg: core.Config{TrustedProxies: []string{"172.30.4.1/32"}}}
	for _, tc := range []struct {
		remote string
		want   bool
	}{{"172.30.4.1:5000", true}, {"172.30.4.2:5000", false}} {
		r := httptest.NewRequest("GET", "http://soseek.example/admin/api/session", nil)
		r.RemoteAddr = tc.remote
		r.Header.Set("X-Forwarded-Proto", "https")
		if got := s.secureRequest(r); got != tc.want {
			t.Fatalf("proxy %s: got %v", tc.remote, got)
		}
	}
}

func TestAdminSecureCookieAndNoBearerSession(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "private", "soseek.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, token, err := db.CreatePrincipal(ctx, "owner", 1000000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.GrantAdmin(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	s := New(core.Config{}, db, nil)
	body, _ := json.Marshal(map[string]string{"token": token})
	r := httptest.NewRequest("POST", "https://soseek.example/admin/api/session", strings.NewReader(string(body)))
	r.Header.Set("Origin", "https://soseek.example")
	r.Header.Set("X-Soseek-Admin", "1")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("login: %d", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing cookie")
	}
	c := cookies[0]
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/admin/api" || c.MaxAge != 43200 {
		t.Fatal("cookie security attributes incorrect")
	}
	if strings.Contains(w.Body.String(), token) || strings.Contains(w.Body.String(), c.Value) {
		t.Fatal("session or access token exposed in login response")
	}
	r = httptest.NewRequest("GET", "https://soseek.example/admin/api/session", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("bearer bypassed session exchange: %d", w.Code)
	}
}
