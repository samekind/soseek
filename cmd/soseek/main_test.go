package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samekind/soseek/internal/client"
	"github.com/samekind/soseek/internal/core"
)

func setupConfig(t *testing.T, server string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SOSEEK_URL", "")
	t.Setenv("SOSEEK_TOKEN_FILE", "")
	dir, _ := client.ConfigDir()
	tokenFile := filepath.Join(dir, "client-token")
	if err := client.WriteSecret(tokenFile, []byte("test-secret-token")); err != nil {
		t.Fatal(err)
	}
	if err := client.WritePrivate(filepath.Join(dir, "client.json"), client.Config{Server: server, TokenFile: tokenFile}); err != nil {
		t.Fatal(err)
	}
}
func invoke(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, stderr bytes.Buffer
	a := &app{in: strings.NewReader(""), out: &out, err: &stderr}
	code := a.run(context.Background(), args)
	return code, out.String(), stderr.String()
}
func oneObject(t *testing.T, s string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		t.Fatalf("invalid JSON output %q: %v", s, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("expected exactly one object: %q", s)
	}
	return obj
}

func TestSubmitFinalJSONAndOptions(t *testing.T) {
	var got core.Request
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret-token" {
			t.Error("missing token")
		}
		if r.URL.Path != "/v1/runs" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		if r.Header.Get("Idempotency-Key") == "" {
			t.Error("missing idempotency key")
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(core.Run{ID: "run_1", Status: "succeeded", Result: &core.Result{RunID: "run_1", Status: "succeeded", Operation: "search", Results: []core.Source{{ID: "S1", URL: "https://example.org", Title: "Result"}}}})
	}))
	defer s.Close()
	setupConfig(t, s.URL)
	code, out, _ := invoke(t, "search", "a query", "--provider", "exa", "--opt", "exa.category=research paper", "--deadline", "10s", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	obj := oneObject(t, out)
	if obj["run_id"] != "run_1" {
		t.Fatal(obj)
	}
	if got.Query != "a query" || got.Provider != "exa" || got.Options["exa.category"] != "research paper" || got.DeadlineSeconds != 10 {
		t.Fatalf("request: %#v", got)
	}
	pending, err := client.PendingList()
	if err != nil || len(pending) != 0 {
		t.Fatalf("acknowledged body remained: %v %v", pending, err)
	}
}
func TestLostSubmissionResumeUsesSameKeyAndBody(t *testing.T) {
	var mu sync.Mutex
	keys := []string{}
	requests := []core.Request{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req core.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		requests = append(requests, req)
		n := len(keys)
		mu.Unlock()
		if n == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		_ = json.NewEncoder(w).Encode(core.Run{ID: "run_original", Status: "queued"})
	}))
	defer s.Close()
	setupConfig(t, s.URL)
	code, out, _ := invoke(t, "search", "private original query", "--json", "--detach")
	if code != 4 {
		t.Fatalf("expected ambiguous submission: %d %s", code, out)
	}
	obj := oneObject(t, out)
	detail := obj["error"].(map[string]any)
	id := detail["pending_id"].(string)
	mu.Lock()
	if len(keys) != 1 {
		t.Error("unexpected automatic submission retry")
	}
	mu.Unlock()
	code, out, _ = invoke(t, "pending", "resume", id, "--json", "--detach")
	if code != 0 {
		t.Fatalf("resume failed: %d %s", code, out)
	}
	if oneObject(t, out)["run_id"] != "run_original" {
		t.Fatal(out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 2 || keys[0] != keys[1] || !reflect.DeepEqual(requests[0], requests[1]) {
		t.Fatalf("changed replay identity/body: %#v %#v", keys, requests)
	}
	p, err := client.LoadPending(id)
	if err != nil || p.Request != nil || p.RunID != "run_original" {
		t.Fatalf("ack %#v %v", p, err)
	}
}
func TestWaitTimeoutNeverCancels(t *testing.T) {
	var paths []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_ = json.NewEncoder(w).Encode(core.Run{ID: "run_wait", Status: "running"})
	}))
	defer s.Close()
	setupConfig(t, s.URL)
	code, out, _ := invoke(t, "runs", "wait", "run_wait", "--wait-timeout", "10ms", "--json")
	if code != 5 {
		t.Fatalf("expected timeout: %d %s", code, out)
	}
	detail := oneObject(t, out)["error"].(map[string]any)
	if detail["run_id"] != "run_wait" {
		t.Fatal(detail)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "/cancel") {
			t.Fatal("waiting cancelled cloud run")
		}
	}
}
func TestLoginStoresPrivateTokenWithoutOutput(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer login-token" {
			t.Error("wrong login token")
		}
		fmt.Fprint(w, `{"schema_version":"1.0"}`)
	}))
	defer s.Close()
	setupConfig(t, s.URL)
	var out, stderr bytes.Buffer
	a := &app{in: strings.NewReader("login-token\n"), out: &out, err: &stderr}
	code := a.run(context.Background(), []string{"auth", "login", "--server", s.URL, "--token-stdin", "--json"})
	if code != 0 {
		t.Fatalf("login %d: %s", code, out.String())
	}
	if strings.Contains(out.String()+stderr.String(), "login-token") {
		t.Fatal("token leaked")
	}
	oneObject(t, out.String())
	c, err := client.Load()
	if err != nil || c.Token != "login-token" {
		t.Fatalf("load %v %v", c, err)
	}
}
func TestStatusExitCodes(t *testing.T) {
	status := "partial"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(core.Run{ID: "run_1", Status: status, Result: &core.Result{RunID: "run_1", Status: status, Results: []core.Source{{URL: "https://example.org"}}}})
	}))
	defer s.Close()
	setupConfig(t, s.URL)
	for _, tc := range []struct {
		status  string
		require bool
		want    int
	}{{"partial", false, 0}, {"partial", true, 4}, {"failed", false, 4}, {"cancelled", false, 6}} {
		status = tc.status
		args := []string{"runs", "get", "run_1", "--json"}
		if tc.require {
			args = append(args, "--require-all")
		}
		code, out, _ := invoke(t, args...)
		if code != tc.want {
			t.Errorf("%s: %d != %d (%s)", tc.status, code, tc.want, out)
		}
		oneObject(t, out)
	}
}
func TestConflictsBeforeNetwork(t *testing.T) {
	for _, args := range [][]string{{"search", "q", "--json", "--stream"}, {"search", "q", "--provider", "exa", "--providers", "tavily"}, {"search", "q", "--channel", "exa-direct"}, {"search", "q", "--budget-usd", "-1"}, {"research", "q", "--mode", "invented"}, {"search", "q", "--opt", "unknown=value"}} {
		code, _, _ := invoke(t, args...)
		if code != 2 {
			t.Errorf("%v gave %d", args, code)
		}
	}
}
func TestNativeCallMapping(t *testing.T) {
	var got core.Request
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(core.Plan{Request: got})
	}))
	defer s.Close()
	setupConfig(t, s.URL)
	input := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(input, []byte(`{"url":"https://example.org"}`), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := invoke(t, "call", "exa.find_similar", "--input", input, "--plan", "--json")
	if code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	if got.Operation != "exa.find_similar" || got.Provider != "exa" || got.Input["url"] != "https://example.org" {
		t.Fatalf("wrong call %#v", got)
	}
}
func TestResultClippingAndANSI(t *testing.T) {
	o := &options{fields: "answer,results.content", maxChars: 6}
	r := core.Result{RunID: "run_1", Status: "succeeded", Answer: "你好世界", Results: []core.Source{{ID: "S1", URL: "https://example.org", Content: "正文内容", Title: "hidden"}}}
	view, err := resultView(r, o, "https://cloud.example")
	if err != nil {
		t.Fatal(err)
	}
	if view["truncated"] != true || view["full_result_command"] != "soseek runs get run_1 --json" {
		t.Fatal(view)
	}
	src := view["results"].([]any)[0].(map[string]any)
	if src["content"] != "正文" || src["url"] != "https://example.org" || src["title"] != nil {
		t.Fatal(src)
	}
	a := &app{secret: "secret-token"}
	s := a.clean("\x1b]8;;https://evil.example\x07title\x1b]8;;\x07\x1b[2J\x1b[31mred\x00 secret-token")
	if s != "titlered [redacted]" {
		t.Fatalf("unsafe terminal text %q", s)
	}
}
func TestPendingCannotResumeDifferentIdentity(t *testing.T) {
	setupConfig(t, "https://cloud.example")
	other, _ := client.New("https://cloud.example", "other-token")
	p, err := client.NewPending(other, core.Request{Operation: "search", Query: "private"})
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ := invoke(t, "pending", "resume", p.ID, "--json")
	if code != 3 {
		t.Fatalf("cross identity resume %d %s", code, out)
	}
}
func TestCancelledLocalContextKeepsRun(t *testing.T) {
	var out, stderr bytes.Buffer
	a := &app{out: &out, err: &stderr, json: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := client.New("https://cloud.example", "test-secret-token")
	code, err := a.wait(ctx, c, core.Run{ID: "run_1", Status: "running"}, &options{waitTimeout: time.Minute})
	if code != 5 || err == nil || !strings.Contains(err.Error(), "not cancelled") {
		t.Fatalf("cancel: %d %v", code, err)
	}
}

func TestStreamEventsFinishBeforeFinalResult(t *testing.T) {
	var ready atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST":
			_ = json.NewEncoder(w).Encode(core.Run{ID: "run_stream", Status: "running"})
		case strings.HasSuffix(r.URL.Path, "/events"):
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "id: 1\nevent: attempt_finished\ndata: {\"status\":\"succeeded\"}\n\n")
			w.(http.Flusher).Flush()
			ready.Store(true)
			<-r.Context().Done()
		default:
			if ready.Load() {
				_ = json.NewEncoder(w).Encode(core.Run{ID: "run_stream", Status: "succeeded", Result: &core.Result{RunID: "run_stream", Status: "succeeded", Results: []core.Source{{URL: "https://example.org"}}}})
			} else {
				_ = json.NewEncoder(w).Encode(core.Run{ID: "run_stream", Status: "running"})
			}
		}
	}))
	defer s.Close()
	setupConfig(t, s.URL)
	code, out, _ := invoke(t, "search", "query", "--stream", "--wait-timeout", "3s")
	if code != 0 {
		t.Fatalf("stream %d: %s", code, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	events, results := 0, 0
	for i, line := range lines {
		obj := oneObject(t, line)
		if obj["type"] == "event" {
			events++
		}
		if obj["type"] == "result" {
			results++
			if i != len(lines)-1 {
				t.Fatal("progress emitted after final result")
			}
		}
	}
	if events != 1 || results != 1 {
		t.Fatalf("events=%d results=%d: %s", events, results, out)
	}
}
func TestDiscoveryArrayWrappedAsOneObject(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[{"id":"search"}]`) }))
	defer s.Close()
	setupConfig(t, s.URL)
	code, out, _ := invoke(t, "operations", "exa", "--json")
	if code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	if oneObject(t, out)["operations"] == nil {
		t.Fatal(out)
	}
}
