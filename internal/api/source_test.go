package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samekind/soseek/internal/config"
	"github.com/samekind/soseek/internal/core"
	"github.com/samekind/soseek/internal/engine"
	"github.com/samekind/soseek/internal/store"
	_ "modernc.org/sqlite"
)

// Source windows are a read-only projection over the stored run result. This
// fixture deliberately never starts workers: fetching a window must not call an
// upstream provider again.
func TestSavedSourceWindowPagingOwnershipAndNoUpstreamCalls(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "private", "source-window.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, ownerToken, err := db.CreatePrincipal(ctx, "owner", 5_000_000, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, otherToken, err := db.CreatePrincipal(ctx, "other", 5_000_000, nil)
	if err != nil {
		t.Fatal(err)
	}
	up := &testUpstream{}
	cfg := core.Config{Revision: "source-window-test"}
	config.Defaults(&cfg)
	handler := New(cfg, db, engine.New(cfg, db, up)).Handler()

	request := func(method, path, token string) (int, []byte) {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code, w.Body.Bytes()
	}
	makeRun := func(key, status string, sources []core.Source) core.Run {
		t.Helper()
		run, _, err := db.Submit(ctx, owner, key, core.Request{Operation: "read", URL: "https://private.example/doc"}, core.Plan{BudgetUSD: "0.100000"}, 5_000_000, 20)
		if err != nil {
			t.Fatal(err)
		}
		if status != "queued" {
			if err := db.Finish(ctx, run.ID, core.Result{Status: status, Operation: "read", Results: sources}); err != nil {
				t.Fatal(err)
			}
		}
		return run
	}

	const fullText = "A😀𐐷BC"
	run := makeRun("saved-source", "succeeded", []core.Source{
		{ID: "S1", URL: "https://private.example/doc", Title: "Private title", PublishedAt: "2026-10-01", Content: fullText, Truncated: true},
		{ID: "S2", URL: "https://private.example/snippet", Snippet: "snippet fallback"},
	})

	status, body := request("GET", fmt.Sprintf("/v1/runs/%s/sources/S1?offset=0&max_chars=2", run.ID), ownerToken)
	if status != http.StatusOK {
		t.Fatalf("first window %d: %s", status, body)
	}
	var first core.SourceWindow
	if err := json.Unmarshal(body, &first); err != nil {
		t.Fatal(err)
	}
	if first.RunID != run.ID || first.SourceID != "S1" || first.Title != "Private title" || first.URL != "https://private.example/doc" || first.PublishedAt != "2026-10-01" || first.Field != "content" || first.Text != "A😀" || first.Offset != 0 || first.ReturnedChars != 2 || first.TotalChars != 5 || first.NextOffset == nil || *first.NextOffset != 2 || !first.Truncated || !first.UpstreamTruncated {
		t.Fatalf("first window lost identity, Unicode boundaries or truncation state: %+v", first)
	}
	status, body = request("GET", fmt.Sprintf("/v1/runs/%s/sources/S1?offset=2&max_chars=3", run.ID), ownerToken)
	if status != http.StatusOK {
		t.Fatalf("final window %d: %s", status, body)
	}
	var last core.SourceWindow
	if err := json.Unmarshal(body, &last); err != nil {
		t.Fatal(err)
	}
	if last.Text != "𐐷BC" || last.Offset != 2 || last.ReturnedChars != 3 || last.TotalChars != 5 || last.NextOffset != nil || !last.Truncated || !last.UpstreamTruncated {
		t.Fatalf("last window did not terminate cleanly: %+v", last)
	}
	status, body = request("GET", fmt.Sprintf("/v1/runs/%s/sources/S1?offset=5&max_chars=3", run.ID), ownerToken)
	if status != http.StatusOK {
		t.Fatalf("end-offset window %d: %s", status, body)
	}
	var atEnd core.SourceWindow
	if err := json.Unmarshal(body, &atEnd); err != nil {
		t.Fatal(err)
	}
	if atEnd.Offset != 5 || atEnd.Text != "" || atEnd.ReturnedChars != 0 || atEnd.NextOffset != nil || !atEnd.Truncated {
		t.Fatalf("offset-at-end window did not terminate: %+v", atEnd)
	}

	status, body = request("GET", fmt.Sprintf("/v1/runs/%s/sources/S2", run.ID), ownerToken)
	if status != http.StatusOK {
		t.Fatalf("snippet fallback %d: %s", status, body)
	}
	var fallback core.SourceWindow
	if err := json.Unmarshal(body, &fallback); err != nil {
		t.Fatal(err)
	}
	if fallback.Field != "snippet" || fallback.Text != "snippet fallback" || fallback.Offset != 0 || fallback.ReturnedChars != len([]rune(fallback.Text)) || fallback.TotalChars != fallback.ReturnedChars || fallback.NextOffset != nil || fallback.Truncated || fallback.UpstreamTruncated {
		t.Fatalf("snippet fallback malformed: %+v", fallback)
	}

	// A different principal must not learn whether this source exists or see its
	// stored private title/body.
	status, body = request("GET", fmt.Sprintf("/v1/runs/%s/sources/S1", run.ID), otherToken)
	if status != http.StatusNotFound || strings.Contains(string(body), "Private title") || strings.Contains(string(body), fullText) {
		t.Fatalf("cross-owner source access leaked data: %d %s", status, body)
	}
	if up.calls.Load() != 0 {
		t.Fatalf("source window caused %d upstream calls", up.calls.Load())
	}
}

func TestSavedSourceWindowErrorsAndBounds(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "private", "source-window-errors.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, token, err := db.CreatePrincipal(ctx, "owner", 5_000_000, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := core.Config{Revision: "source-window-errors"}
	config.Defaults(&cfg)
	handler := New(cfg, db, engine.New(cfg, db, &testUpstream{})).Handler()
	request := func(path string) (int, []byte) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code, w.Body.Bytes()
	}
	pending, _, err := db.Submit(ctx, owner, "pending-source-window", core.Request{Operation: "read", URL: "https://private.example/pending"}, core.Plan{BudgetUSD: "0.100000"}, 5_000_000, 20)
	if err != nil {
		t.Fatal(err)
	}
	status, _ := request(fmt.Sprintf("/v1/runs/%s/sources/S1", pending.ID))
	if status != http.StatusConflict {
		t.Fatalf("pending run source window status=%d, want 409", status)
	}
	completed, _, err := db.Submit(ctx, owner, "completed-source-window", core.Request{Operation: "read", URL: "https://private.example/doc"}, core.Plan{BudgetUSD: "0.100000"}, 5_000_000, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Finish(ctx, completed.ID, core.Result{Status: "succeeded", Results: []core.Source{{ID: "S1", URL: "https://private.example/doc", Content: "text"}}}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"offset=-1", "offset=5", "offset=nope", "offset=1&offset=2", "max_chars=0", "max_chars=20001", "max_chars=nope"} {
		status, _ := request(fmt.Sprintf("/v1/runs/%s/sources/S1?%s", completed.ID, query))
		if status != http.StatusBadRequest {
			t.Errorf("query %q status=%d, want 400", query, status)
		}
	}
	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/runs/%s/sources/S1", completed.ID), nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated source window status=%d, want 401", unauthenticated.Code)
	}
	for _, path := range []string{fmt.Sprintf("/v1/runs/%s/sources/S9", completed.ID), "/v1/runs/missing-run-id/sources/S1"} {
		status, _ := request(path)
		if status != http.StatusNotFound {
			t.Errorf("not found path %q status=%d, want 404", path, status)
		}
	}
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rawDB.Close()
	if _, err := rawDB.ExecContext(ctx, `UPDATE runs SET expired=1 WHERE id=?`, completed.ID); err != nil {
		t.Fatalf("mark result expired: %v", err)
	}
	status, _ = request(fmt.Sprintf("/v1/runs/%s/sources/S1", completed.ID))
	if status != http.StatusGone {
		t.Fatalf("expired source window status=%d, want 410", status)
	}
}
