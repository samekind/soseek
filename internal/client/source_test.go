package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/samekind/soseek/internal/core"
)

func TestSourceFallbackOnlyReadsSavedResult(t *testing.T) {
	for _, status := range []int{200, 404, 401, 410} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" {
					t.Fatalf("must never submit: %s", r.Method)
				}
				switch r.URL.Path {
				case "/v1/runs/run_1/sources/s1":
					if r.URL.Query().Get("offset") != "1" || r.URL.Query().Get("max_chars") != "2" {
						t.Error("window parameters lost")
					}
					if status != 200 {
						w.WriteHeader(status)
						return
					}
					out, _ := core.WindowSource("run_1", core.Source{ID: "s1", Content: "一🙂二三"}, 1, 2)
					json.NewEncoder(w).Encode(out)
				case "/v1/runs/run_1/result":
					json.NewEncoder(w).Encode(core.Result{RunID: "run_1", Status: "succeeded", Results: []core.Source{{ID: "s1", Content: "一🙂二三"}}})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer s.Close()
			c, _ := New(s.URL, "token")
			out, err := c.Source(context.Background(), "run_1", "s1", 1, 2)
			if status == 200 || status == 404 {
				if err != nil || out.Text != "🙂二" || out.NextOffset == nil || *out.NextOffset != 3 {
					t.Fatalf("%+v %v", out, err)
				}
			} else {
				var ae *APIError
				if !errors.As(err, &ae) || ae.Status != status {
					t.Fatalf("%v", err)
				}
			}
			want := 1
			if status == 404 {
				want = 2
			}
			if calls != want {
				t.Fatalf("calls=%d want %d", calls, want)
			}
		})
	}
}

func TestSourceRejectsInvalidParametersLocally(t *testing.T) {
	c, _ := New("https://example.org", "token")
	for _, pair := range [][2]int{{-1, 1}, {0, -1}, {0, 20001}} {
		_, err := c.Source(context.Background(), "run_1", "s1", pair[0], pair[1])
		var ce *core.Error
		if !errors.As(err, &ce) || ce.Code != "invalid_request" {
			t.Fatalf("%v: %v", pair, err)
		}
	}
}
