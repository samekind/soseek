package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samekind/soseek/internal/config"
	"github.com/samekind/soseek/internal/core"
	"github.com/samekind/soseek/internal/engine"
	"github.com/samekind/soseek/internal/store"
)

type testUpstream struct{ calls atomic.Int32 }

func (u *testUpstream) Call(ctx context.Context, c core.Channel, call core.Call) (core.Output, error) {
	u.calls.Add(1)
	return core.Output{Sources: []core.Source{{URL: "https://example.org/document", Title: "Source", Occurrences: []core.Occurrence{{Provider: c.Provider, Channel: c.ID, Rank: 1}}}}, Usage: core.Usage{CostKnown: true, ReportedMicros: 7000}}, nil
}
func (u *testUpstream) Complete(context.Context, core.Channel, []core.Message, []core.Tool, int) (core.Completion, error) {
	return core.Completion{}, core.E("not_supported", "test does not use models")
}
func (u *testUpstream) Operations(p string) []core.Operation { return []core.Operation{{ID: "search"}} }

func TestHTTPRunIsolationIdempotencyAndEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, e := store.Open(filepath.Join(t.TempDir(), "private", "tasks.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	_, ta, e := db.CreatePrincipal(ctx, "A", 5000000, nil)
	if e != nil {
		t.Fatal(e)
	}
	_, tb, e := db.CreatePrincipal(ctx, "B", 5000000, nil)
	if e != nil {
		t.Fatal(e)
	}
	cfg := core.Config{Revision: "test", ArtifactDir: filepath.Join(t.TempDir(), "artifacts"), SearchOrder: []string{"exa"}, Channels: []core.Channel{{ID: "test", Provider: "exa", BaseURL: "https://example.org", Operations: []string{"search"}, CostBounds: map[string]int64{"search": 50000}, Concurrency: 2, RPM: 1000, TimeoutSeconds: 10}}}
	config.Defaults(&cfg)
	up := &testUpstream{}
	server := New(cfg, db, engine.New(cfg, db, up))
	server.Start(ctx)
	defer func() { cancel(); server.Wait() }()
	h := httptest.NewServer(server.Handler())
	defer h.Close()
	request := func(method, path, token, key, body string) (int, []byte) {
		t.Helper()
		r, _ := http.NewRequest(method, h.URL+path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		res, e := h.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, e := io.ReadAll(res.Body)
		if e != nil {
			t.Fatal(e)
		}
		return res.StatusCode, b
	}
	if status, _ := request("GET", "/v1/capabilities", "", "", ""); status != 401 {
		t.Fatal("unauthenticated discovery", status)
	}
	body := `{"operation":"search","query":"test"}`
	status, b := request("POST", "/v1/runs", ta, "acceptance-key", body)
	if status != 202 {
		t.Fatalf("submit %d: %s", status, b)
	}
	var run core.Run
	json.Unmarshal(b, &run)
	_, b = request("POST", "/v1/runs", ta, "acceptance-key", body)
	var again core.Run
	json.Unmarshal(b, &again)
	if again.ID != run.ID {
		t.Fatal("idempotent request duplicated")
	}
	if status, _ = request("POST", "/v1/runs", ta, "acceptance-key", `{"operation":"search","query":"changed"}`); status != 409 {
		t.Fatal("payload mismatch", status)
	}
	for _, suffix := range []string{"", "/result", "/events"} {
		if status, _ = request("GET", "/v1/runs/"+run.ID+suffix, tb, "", ""); status != 404 {
			t.Fatalf("cross user read %s: %d", suffix, status)
		}
	}
	if status, _ = request("GET", "/v1/artifacts/"+run.ID, tb, "", ""); status != 404 {
		t.Fatal("cross user artifact", status)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, b = request("GET", "/v1/runs/"+run.ID, ta, "", "")
		json.Unmarshal(b, &run)
		if core.Terminal(run.Status) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.Status != "succeeded" || up.calls.Load() != 1 {
		t.Fatalf("run=%+v upstream calls=%d", run, up.calls.Load())
	}
	status, b = request("GET", "/v1/runs/"+run.ID+"/result", ta, "", "")
	var result core.Result
	json.Unmarshal(b, &result)
	if status != 200 || len(result.Results) != 1 || result.Usage.ReportedMicros != 7000 {
		t.Fatalf("result %d %s", status, b)
	}
	status, b = request("GET", "/v1/runs/"+run.ID+"/events", ta, "", "")
	if status != 200 || !bytes.Contains(b, []byte("event: accepted")) || !bytes.Contains(b, []byte("id: ")) {
		t.Fatalf("events %d %s", status, b)
	}
	// A previously accepted request survives a policy/configuration change.
	cfg.SearchOrder = []string{"missing"}
	cfg.Channels = nil
	cfg.Revision = "changed"
	changed := New(cfg, db, engine.New(cfg, db, up))
	r := httptest.NewRequest("POST", "/v1/runs", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+ta)
	r.Header.Set("Idempotency-Key", "acceptance-key")
	w := httptest.NewRecorder()
	changed.Handler().ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatalf("old request failed after policy change: %d %s", w.Code, w.Body.String())
	}
}
