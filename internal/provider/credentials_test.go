package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/samekind/soseek/internal/core"
	"github.com/samekind/soseek/internal/credentials"
)

func TestCredentialManagerRotationAppliesToCallAndComplete(t *testing.T) {
	var hits atomic.Int32
	var expected atomic.Value
	expected.Store("original-private-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/search":
			if r.Header.Get("X-API-Key") != expected.Load().(string) {
				t.Error("search used stale credential")
			}
			_, _ = io.WriteString(w, `{"results":[{"url":"https://go.dev/"}],"costDollars":{"total":0}}`)
		case "/chat/completions":
			if r.Header.Get("Authorization") != "Bearer "+expected.Load().(string) {
				t.Error("completion used stale credential")
			}
			_, _ = io.WriteString(w, `{"model":"grok-test","choices":[{"message":{"role":"assistant","content":"ready"},"finish_reason":"stop"}]}`)
		default:
			t.Error("unexpected request path")
		}
	}))
	defer server.Close()
	keyFile := filepath.Join(t.TempDir(), "private-deployment-key")
	if err := os.WriteFile(keyFile, []byte("original-private-key"), 0600); err != nil {
		t.Fatal(err)
	}
	exa := core.Channel{ID: "exa", Provider: "exa", BaseURL: server.URL, KeyFile: keyFile, TimeoutSeconds: 2}
	grok := core.Channel{ID: "grok", Provider: "grok", BaseURL: server.URL, KeyFile: keyFile, Model: "grok-test", TimeoutSeconds: 2}
	m, err := credentials.New(core.Config{Database: filepath.Join(t.TempDir(), "service.db"), Channels: []core.Channel{exa, grok}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	client := NewWithKeyResolver(m.ReadKey)
	callBoth := func(wantSuccess bool) {
		t.Helper()
		before := hits.Load()
		_, searchErr := client.Call(context.Background(), exa, core.Call{Operation: "search", Query: "Go"})
		_, completeErr := client.Complete(context.Background(), grok, []core.Message{{Role: "user", Content: "hi"}}, nil, 64)
		for _, err := range []error{searchErr, completeErr} {
			if wantSuccess && err != nil {
				t.Fatal(err)
			}
			if !wantSuccess && (err == nil || strings.Contains(err.Error(), "original-private-key") || strings.Contains(err.Error(), "rotated-private-key") || strings.Contains(err.Error(), keyFile)) {
				t.Fatalf("disabled key was used or exposed: %v", err)
			}
		}
		if !wantSuccess && hits.Load() != before {
			t.Fatal("disabled key caused an upstream request")
		}
	}
	callBoth(true)
	list, err := m.List()
	if err != nil || len(list) != 1 {
		t.Fatal("shared credential was not grouped")
	}
	if err = m.Set(list[0].ID, "rotated-private-key"); err != nil {
		t.Fatal(err)
	}
	expected.Store("rotated-private-key")
	callBoth(true)
	if err = m.Disable(list[0].ID); err != nil {
		t.Fatal(err)
	}
	callBoth(false)
	if err = m.Reset(list[0].ID); err != nil {
		t.Fatal(err)
	}
	expected.Store("original-private-key")
	callBoth(true)
}

func TestResolverErrorsAreSanitizedAndNeverFallBack(t *testing.T) {
	ch := channel(t, "grok", "http://127.0.0.1:1")
	var resolverCalls atomic.Int32
	client := NewWithHTTPAndKeyResolver(&http.Client{}, func(core.Channel) (string, error) {
		resolverCalls.Add(1)
		return "", errors.New("secret-value /private/key/path ENV_SECRET_NAME")
	})
	_, searchErr := client.Call(context.Background(), ch, core.Call{Operation: "search", Query: "Go"})
	_, completionErr := client.Complete(context.Background(), ch, []core.Message{{Role: "user", Content: "hi"}}, nil, 64)
	for _, err := range []error{searchErr, completionErr} {
		var e *core.Error
		if !errors.As(err, &e) || e.Code != "credential_error" || strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), "/private") || strings.Contains(err.Error(), "ENV_SECRET_NAME") {
			t.Fatalf("resolver error not sanitized: %v", err)
		}
	}
	if resolverCalls.Load() != 2 {
		t.Fatal("not every path resolved its credential")
	}
}
