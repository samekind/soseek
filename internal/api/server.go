// Package api exposes the authenticated, principal-scoped soseek run protocol.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/samekind/soseek/internal/core"
	"github.com/samekind/soseek/internal/credentials"
	"github.com/samekind/soseek/internal/engine"
	"github.com/samekind/soseek/internal/store"
	"github.com/samekind/soseek/schemas"
)

type Server struct {
	cfg         core.Config
	db          *store.Store
	engine      *engine.Engine
	wg          sync.WaitGroup
	credentials *credentials.Manager
	loginMu     sync.Mutex
	loginWindow time.Time
	loginCount  int
}

func New(cfg core.Config, db *store.Store, e *engine.Engine) *Server {
	return &Server{cfg: cfg, db: db, engine: e}
}

type principalKey struct{}

func principal(r *http.Request) core.Principal {
	return r.Context().Value(principalKey{}).(core.Principal)
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerAdmin(mux)
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) {
		b, _ := schemas.Files.ReadFile("openapi.json")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"status": "ok", "version": core.Version})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.db.ListPrincipals(r.Context()); err != nil {
			reply(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		reply(w, 200, map[string]string{"status": "ready"})
	})
	mux.Handle("GET /v1/capabilities", s.auth(http.HandlerFunc(s.capabilities)))
	mux.Handle("GET /v1/providers", s.auth(http.HandlerFunc(s.capabilities)))
	mux.Handle("GET /v1/profiles", s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, part(s.engine.Capabilities(principal(r)), "profiles"))
	})))
	mux.Handle("GET /v1/operations/{provider}", s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caps := part(s.engine.Capabilities(principal(r)), "providers")
		m, _ := caps.(map[string][]core.Operation)
		ops, ok := m[r.PathValue("provider")]
		if !ok {
			fail(w, core.E("not_found", "provider not available"))
			return
		}
		reply(w, 200, ops)
	})))
	mux.Handle("POST /v1/plans", s.auth(http.HandlerFunc(s.plan)))
	mux.Handle("POST /v1/runs", s.auth(http.HandlerFunc(s.submit)))
	mux.Handle("GET /v1/runs/{id}", s.auth(http.HandlerFunc(s.getRun)))
	mux.Handle("GET /v1/runs/{id}/result", s.auth(http.HandlerFunc(s.result)))
	mux.Handle("GET /v1/runs/{id}/sources/{source}", s.auth(http.HandlerFunc(s.source)))
	mux.Handle("POST /v1/runs/{id}/cancel", s.auth(http.HandlerFunc(s.cancel)))
	mux.Handle("GET /v1/runs/{id}/events", s.auth(http.HandlerFunc(s.events)))
	mux.Handle("GET /v1/artifacts/{id}", s.auth(http.HandlerFunc(s.artifact)))
	mux.Handle("GET /v1/usage", s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, e := s.db.Usage(r.Context(), principal(r).ID)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, u)
	})))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Soseek-Revision", s.cfg.Revision)
		defer func() {
			if recover() != nil {
				log.Print("request panic recovered")
				reply(w, 500, map[string]any{"error": core.E("internal_error", "request could not be completed")})
			}
		}()
		mux.ServeHTTP(w, r)
	})
}
func part(v any, key string) any {
	if m, ok := v.(map[string]any); ok {
		return m[key]
	}
	return nil
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	w.Header().Set("ETag", fmt.Sprintf("%q", s.cfg.Revision+":"+p.ID))
	reply(w, http.StatusOK, s.engine.Capabilities(p))
}
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := r.Header.Get("Authorization")
		if !strings.HasPrefix(a, "Bearer ") || len(a) > 512 {
			fail(w, core.E("unauthorized", "valid bearer token required"))
			return
		}
		p, err := s.db.Authenticate(r.Context(), strings.TrimPrefix(a, "Bearer "))
		if err != nil {
			fail(w, core.E("unauthorized", "valid bearer token required"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return core.E("invalid_request", "Content-Type must be application/json")
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return core.E("invalid_request", "request must be a valid JSON object with known fields")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return core.E("invalid_request", "request must contain one JSON object")
	}
	return nil
}
func (s *Server) plan(w http.ResponseWriter, r *http.Request) {
	var req core.Request
	if e := decode(w, r, &req); e != nil {
		fail(w, e)
		return
	}
	p, e := s.engine.Plan(req, principal(r))
	if e != nil {
		fail(w, e)
		return
	}
	reply(w, 200, p)
}
func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	var req core.Request
	if e := decode(w, r, &req); e != nil {
		fail(w, e)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 8 || len(key) > 128 || strings.ContainsAny(key, "\r\n\t ") {
		fail(w, core.E("invalid_request", "Idempotency-Key must be 8-128 non-space characters"))
		return
	}
	p := principal(r)
	if run, exists, err := s.db.LookupIdempotent(r.Context(), p.ID, key, req); err != nil {
		fail(w, err)
		return
	} else if exists {
		w.Header().Set("Idempotency-Replayed", "true")
		w.Header().Set("Location", "/v1/runs/"+run.ID)
		reply(w, http.StatusAccepted, run)
		return
	}
	plan, e := s.engine.Plan(req, p)
	if e != nil {
		fail(w, e)
		return
	}
	run, existing, e := s.db.Submit(r.Context(), p, key, req, plan, s.cfg.Limits.DailyMicros, s.cfg.Limits.Queue)
	if e != nil {
		fail(w, e)
		return
	}
	w.Header().Set("Location", "/v1/runs/"+run.ID)
	if existing {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	reply(w, 202, run)
}
func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	run, e := s.db.GetRun(r.Context(), principal(r).ID, r.PathValue("id"))
	if e != nil {
		fail(w, e)
		return
	}
	reply(w, 200, run)
}
func (s *Server) result(w http.ResponseWriter, r *http.Request) {
	run, e := s.db.GetRun(r.Context(), principal(r).ID, r.PathValue("id"))
	if e != nil {
		fail(w, e)
		return
	}
	if run.Result == nil {
		reply(w, 202, run)
		return
	}
	reply(w, 200, run.Result)
}
func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if e := s.db.Cancel(r.Context(), principal(r).ID, id); e != nil {
		fail(w, e)
		return
	}
	s.getRun(w, r)
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	id := r.PathValue("id")
	if _, e := s.db.GetRun(r.Context(), p.ID, id); e != nil {
		fail(w, e)
		return
	}
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = r.URL.Query().Get("after")
	}
	var after int64
	var err error
	if cursor != "" {
		after, err = strconv.ParseInt(cursor, 10, 64)
		if err != nil || after < 0 {
			fail(w, core.E("invalid_request", "invalid event cursor"))
			return
		}
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		fail(w, core.E("internal_error", "streaming unavailable"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	beat := time.NewTicker(15 * time.Second)
	defer beat.Stop()
	for {
		events, e := s.db.Events(r.Context(), p.ID, id, after)
		if e != nil {
			return
		}
		for _, v := range events {
			if _, e = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", v.ID, v.Type, v.Data); e != nil {
				return
			}
			after = v.ID
		}
		fl.Flush()
		run, e := s.db.GetRun(r.Context(), p.ID, id)
		if e != nil {
			return
		}
		if core.Terminal(run.Status) {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			fl.Flush()
		case <-tick.C:
		}
	}
}
func (s *Server) artifact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, e := s.db.GetRun(r.Context(), principal(r).ID, id)
	if e != nil {
		fail(w, e)
		return
	}
	if run.Result == nil {
		fail(w, core.E("not_found", "result is not ready"))
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", id+".json"))
	reply(w, 200, run.Result)
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, err error) {
	var ce *core.Error
	if !errors.As(err, &ce) {
		ce = core.E("internal_error", "operation could not be completed")
	}
	status := 400
	switch ce.Code {
	case "unauthorized":
		status = 401
	case "forbidden":
		status = 403
	case "not_found":
		status = 404
	case "gone":
		status = 410
	case "idempotency_conflict", "conflict":
		status = 409
	case "budget_exceeded", "queue_full", "rate_limited":
		status = 429
	case "internal_error":
		status = 500
	case "planner_unavailable", "no_available_channel":
		status = 503
	}
	if ce.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(ce.RetryAfterSeconds))
	}
	reply(w, status, map[string]any{"error": ce})
}

func (s *Server) Start(ctx context.Context) {
	for i := 0; i < s.cfg.Limits.Workers; i++ {
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.worker(ctx) }()
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if e := s.db.Cleanup(ctx, s.cfg.Limits.RetentionDays); e != nil && ctx.Err() == nil {
				log.Print("retention cleanup failed")
			}
			s.cleanArtifacts()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func (s *Server) Wait() { s.wg.Wait() }
func (s *Server) worker(ctx context.Context) {
	for ctx.Err() == nil {
		run, e := s.db.Claim(ctx)
		if e != nil || run == nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}
		func() {
			rctx, cancel := context.WithDeadline(ctx, run.Deadline)
			defer cancel()
			finished := make(chan struct{})
			defer close(finished)
			go func() {
				t := time.NewTicker(250 * time.Millisecond)
				defer t.Stop()
				for {
					select {
					case <-finished:
						return
					case <-rctx.Done():
						return
					case <-t.C:
						if s.db.Cancelled(rctx, run.ID) {
							cancel()
							return
						}
					}
				}
			}()
			var result core.Result
			if run.Revision != s.cfg.Revision {
				result = core.Result{SchemaVersion: core.SchemaVersion, RunID: run.ID, Operation: run.Request.Operation, Status: "failed", Warnings: []string{"Configuration changed after acceptance; request was not replayed. Submit a new request after reviewing its plan."}, StopReason: "configuration_changed"}
			} else {
				result = s.engine.Execute(rctx, *run)
			}
			finishCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
			defer c()
			if e := s.db.Finish(finishCtx, run.ID, result); e != nil {
				log.Printf("run %s: result persistence failed", run.ID)
				return
			}
			if e := s.saveArtifact(run.ID, result); e != nil {
				log.Printf("run %s: artifact export failed; database result retained", run.ID)
			}
		}()
	}
}
func (s *Server) saveArtifact(id string, result core.Result) error {
	if filepath.Base(id) != id {
		return fmt.Errorf("invalid run ID")
	}
	if e := os.MkdirAll(s.cfg.ArtifactDir, 0700); e != nil {
		return e
	}
	b, e := json.Marshal(result)
	if e != nil {
		return e
	}
	tmp, e := os.CreateTemp(s.cfg.ArtifactDir, ".result-*")
	if e != nil {
		return e
	}
	defer os.Remove(tmp.Name())
	if _, e = tmp.Write(b); e != nil {
		tmp.Close()
		return e
	}
	if e = tmp.Sync(); e != nil {
		tmp.Close()
		return e
	}
	if e = tmp.Close(); e != nil {
		return e
	}
	return os.Rename(tmp.Name(), filepath.Join(s.cfg.ArtifactDir, id+".json"))
}
func (s *Server) cleanArtifacts() {
	entries, e := os.ReadDir(s.cfg.ArtifactDir)
	if e != nil {
		return
	}
	cut := time.Now().Add(-time.Duration(s.cfg.Limits.RetentionDays) * 24 * time.Hour)
	for _, v := range entries {
		if v.IsDir() || !strings.HasSuffix(v.Name(), ".json") {
			continue
		}
		info, e := v.Info()
		if e == nil && info.ModTime().Before(cut) {
			_ = os.Remove(filepath.Join(s.cfg.ArtifactDir, v.Name()))
		}
	}
}
