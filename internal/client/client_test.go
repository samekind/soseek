package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jstar0/soseek/internal/core"
)

func TestValidateURL(t *testing.T) {
	for _, raw := range []string{"https://cloud.example", "http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		if _, err := ValidateURL(raw); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{"http://cloud.example", "https://user:secret@cloud.example", "https://cloud.example?q=secret", "ftp://cloud.example", "http://localhost.evil.example", ""} {
		if _, err := ValidateURL(raw); err == nil {
			t.Errorf("accepted unsafe URL %s", raw)
		}
	}
}
func TestNeverRedirectAuthorization(t *testing.T) {
	gotTarget := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTarget = true
		t.Error("redirect target must not be contacted")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Error("missing authorization")
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	c, _ := New(source.URL, "secret-token")
	var result any
	err := c.Do(context.Background(), "GET", "/v1/capabilities", nil, &result, nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 307 {
		t.Fatalf("expected rejected redirect, got %v", err)
	}
	if gotTarget {
		t.Fatal("followed redirect")
	}
}
func TestErrorRedactionAndRetryAfter(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":{"code":"rate_limited","message":"token secret-token rejected"}}`)
	}))
	defer s.Close()
	c, _ := New(s.URL, "secret-token")
	var result any
	err := c.Do(context.Background(), "GET", "/v1/usage", nil, &result, nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Detail.RetryAfterSeconds != 60 || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("unsafe/wrong API error: %v", err)
	}
}
func TestEventsCursor(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Last-Event-ID") != "9" {
			t.Error("missing event cursor")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": keepalive\n\nid: 10\nevent: step\ndata: {\ndata: \"status\":\"done\"}\n\n")
	}))
	defer s.Close()
	c, _ := New(s.URL, "secret-token")
	var got []core.Event
	err := c.Events(context.Background(), "run_1", 9, func(e core.Event) error { got = append(got, e); return nil })
	if err != nil || len(got) != 1 || got[0].ID != 10 || got[0].Type != "step" || !json.Valid(got[0].Data) {
		t.Fatalf("events: %#v, %v", got, err)
	}
}
func TestPendingPrivateAckAndExpiry(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c, _ := New("https://cloud.example", "secret-token")
	p, err := NewPending(c, core.Request{Operation: "search", Query: "private query"})
	if err != nil {
		t.Fatal(err)
	}
	path, _ := pendingPath(p.ID)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "private query") || strings.Contains(string(b), "secret-token") {
		t.Fatal("pending omitted original request or saved token")
	}
	info, _ := os.Stat(path)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v", info.Mode())
	}
	if err = Acknowledge(&p, "run_123"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "private query") || !strings.Contains(string(b), "run_123") {
		t.Fatal("acknowledgement retained request body")
	}
	p2, err := NewPending(c, core.Request{Operation: "search", Query: "expired"})
	if err != nil {
		t.Fatal(err)
	}
	p2.CreatedAt = time.Now().Add(-25 * time.Hour)
	if err = SavePending(p2); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadPending(p2.ID); err == nil {
		t.Fatal("expired pending request accepted")
	}
	path2, _ := pendingPath(p2.ID)
	if _, err = os.Stat(path2); !os.IsNotExist(err) {
		t.Fatal("expired body not removed")
	}
}
func TestPrivateFilePermissionAndAtomicReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "token")
	if err := WriteSecret(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := WriteSecret(path, []byte("second")); err != nil {
		t.Fatal(err)
	}
	b, err := ReadPrivate(path)
	if err != nil || string(b) != "second" {
		t.Fatalf("read %q %v", b, err)
	}
	if runtime.GOOS != "windows" {
		if err = os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err = ReadPrivate(path); err == nil {
			t.Fatal("read world-readable secret")
		}
	}
}

func TestDiscoveryCacheIsScopedAndRevalidated(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("If-None-Match") == `"r1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"r1"`)
		fmt.Fprint(w, `{"revision":"r1"}`)
	}))
	defer s.Close()
	c, _ := New(s.URL, "first-token")
	var out any
	if err := c.Discovery(context.Background(), "/v1/capabilities", &out, false); err != nil {
		t.Fatal(err)
	}
	if err := c.Discovery(context.Background(), "/v1/capabilities", &out, false); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("fresh cache not used")
	}
	dir, _ := StateDir()
	files, err := filepath.Glob(filepath.Join(dir, "discovery", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	b, _ := ReadPrivate(files[0])
	var cache discoveryCache
	_ = json.Unmarshal(b, &cache)
	cache.ExpiresAt = time.Now().Add(-time.Minute)
	if err = WritePrivate(files[0], cache); err != nil {
		t.Fatal(err)
	}
	if err = c.Discovery(context.Background(), "/v1/capabilities", &out, false); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("stale cache not revalidated")
	}
	other, _ := New(s.URL, "second-token")
	if err = other.Discovery(context.Background(), "/v1/capabilities", &out, false); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("discovery cache leaked between identities")
	}
	if err = c.Discovery(context.Background(), "/v1/capabilities", &out, true); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatal("explicit fresh did not bypass cache")
	}
}
