package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samekind/soseek/internal/core"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, e := Open(filepath.Join(t.TempDir(), "private", "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func person(t *testing.T, s *Store, name string, daily int64) core.Principal {
	t.Helper()
	p, _, e := s.CreatePrincipal(context.Background(), name, daily, nil)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func submit(t *testing.T, s *Store, p core.Principal, key string, budget int64) core.Run {
	t.Helper()
	r, _, e := s.Submit(context.Background(), p, key, core.Request{Operation: "search", Query: key, DeadlineSeconds: 300}, core.Plan{BudgetUSD: core.USD(budget)}, 1000000, 100)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func claim(t *testing.T, s *Store) core.Run {
	t.Helper()
	r, e := s.Claim(context.Background())
	if e != nil || r == nil {
		t.Fatalf("claim: %v, %v", r, e)
	}
	return *r
}
func requireCode(t *testing.T, e error, code string) {
	t.Helper()
	var apiErr *core.Error
	if !errors.As(e, &apiErr) || apiErr.Code != code {
		t.Fatalf("want %s, got %v", code, e)
	}
}

func TestAuthenticationIsolationAndRevoke(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p, token, e := s.CreatePrincipal(ctx, "alice", 1000, []string{"exa"})
	if e != nil {
		t.Fatal(e)
	}
	if len(token) < 43 {
		t.Fatal("token lacks entropy")
	}
	authed, e := s.Authenticate(ctx, token)
	if e != nil || authed.ID != p.ID || !authed.Allows("exa") || authed.Allows("firecrawl") {
		t.Fatalf("authentication: %#v, %v", authed, e)
	}
	_, e = s.Authenticate(ctx, "incorrect")
	requireCode(t, e, "unauthorized")
	var stored string
	if e = s.db.QueryRow(`SELECT token_hash FROM principals WHERE id=?`, p.ID).Scan(&stored); e != nil {
		t.Fatal(e)
	}
	if stored == token || len(stored) != 64 {
		t.Fatal("plaintext token stored")
	}
	bob := person(t, s, "bob", 1000)
	r := submit(t, s, p, "private-query", 100)
	_, e = s.GetRun(ctx, bob.ID, r.ID)
	requireCode(t, e, "not_found")
	_, e = s.Events(ctx, bob.ID, r.ID, 0)
	requireCode(t, e, "not_found")
	e = s.Cancel(ctx, bob.ID, r.ID)
	requireCode(t, e, "not_found")
	if e = s.Revoke(ctx, p.ID); e != nil {
		t.Fatal(e)
	}
	_, e = s.Authenticate(ctx, token)
	requireCode(t, e, "unauthorized")
	r, e = s.GetRun(ctx, p.ID, r.ID)
	if e != nil || r.Status != "cancelled" {
		t.Fatalf("queued revoke: %#v, %v", r, e)
	}
	info, e := os.Stat(s.path)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("database permissions: %v, %v", info, e)
	}
	info, e = os.Stat(filepath.Dir(s.path))
	if e != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("directory permissions: %v, %v", info, e)
	}
}

func TestConcurrentDailyReservation(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p := person(t, s, "alice", 300)
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, e := s.Submit(ctx, p, strings.Repeat("x", i+1), core.Request{Operation: "search", Query: "q"}, core.Plan{BudgetUSD: core.USD(100)}, 1000, 100)
			if e == nil {
				accepted.Add(1)
			} else {
				var ce *core.Error
				if !errors.As(e, &ce) || ce.Code != "budget_exceeded" {
					t.Errorf("unexpected reservation failure: %v", e)
				}
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 3 {
		t.Fatalf("budget oversubscription: %d accepted", accepted.Load())
	}
	u, e := s.Usage(ctx, p.ID)
	if e != nil || u["reserved_micros"].(int64) != 300 {
		t.Fatalf("usage: %v, %v", u, e)
	}
	bob := person(t, s, "bob", 1000)
	_, _, e = s.Submit(ctx, bob, "global", core.Request{Operation: "search"}, core.Plan{BudgetUSD: core.USD(100)}, 350, 100)
	requireCode(t, e, "budget_exceeded")
}

func TestConcurrentIdempotencyAndDifferentPayload(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p := person(t, s, "alice", 1000)
	req := core.Request{Operation: "search", Query: "same", Options: map[string]any{"b": true, "a": 2}}
	plan := core.Plan{BudgetUSD: core.USD(100)}
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	var created atomic.Int64
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, existing, e := s.Submit(ctx, p, "same-key", req, plan, 1000, 100)
			if e != nil {
				t.Error(e)
				return
			}
			if !existing {
				created.Add(1)
			}
			ids <- r.ID
		}()
	}
	wg.Wait()
	close(ids)
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("idempotent request created different tasks")
		}
	}
	if created.Load() != 1 {
		t.Fatalf("created %d runs", created.Load())
	}
	req.Query = "different"
	_, _, e := s.Submit(ctx, p, "same-key", req, plan, 1000, 100)
	requireCode(t, e, "idempotency_conflict")
	bob := person(t, s, "bob", 1000)
	r, existing, e := s.Submit(ctx, bob, "same-key", req, plan, 1000, 100)
	if e != nil || existing || r.ID == first {
		t.Fatalf("key not principal scoped: %v, %v", r, e)
	}
}

func TestClaimFairnessAndOnePerPrincipal(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a := person(t, s, "alice", 1000)
	b := person(t, s, "bob", 1000)
	r1 := submit(t, s, a, "a1", 100)
	submit(t, s, a, "a2", 100)
	r3 := submit(t, s, b, "b1", 100)
	first := claim(t, s)
	if first.ID != r1.ID {
		t.Fatal("first queued task should run first")
	}
	second := claim(t, s)
	if second.ID != r3.ID {
		t.Fatal("alice's second task starved bob")
	}
	none, e := s.Claim(ctx)
	if e != nil || none != nil {
		t.Fatalf("claimed concurrent task for same principal: %v %v", none, e)
	}
	if e = s.Finish(ctx, first.ID, core.Result{Status: "succeeded"}); e != nil {
		t.Fatal(e)
	}
	third := claim(t, s)
	if third.PrincipalID != a.ID {
		t.Fatal("alice did not resume")
	}
}

func TestAttemptAccountingUnknownKnownZeroAndRefund(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p := person(t, s, "alice", 1000)
	r := submit(t, s, p, "q", 100)
	claim(t, s)
	a, e := s.BeginAttempt(ctx, r.ID, core.Attempt{Provider: "exa", Operation: "search", MaxMicros: 60})
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.BeginAttempt(ctx, r.ID, core.Attempt{MaxMicros: 41})
	requireCode(t, e, "budget_exceeded")
	a.Status = "succeeded"
	a.Usage = core.Usage{ReportedMicros: 20, EstimatedMicros: 50}
	if e = s.EndAttempt(ctx, a); e != nil {
		t.Fatal(e)
	}
	b, e := s.BeginAttempt(ctx, r.ID, core.Attempt{MaxMicros: 80})
	if e != nil {
		t.Fatal(e)
	}
	b.Status = "failed"
	b.Usage = core.Usage{CostKnown: true}
	if e = s.EndAttempt(ctx, b); e != nil {
		t.Fatal(e)
	}
	c, e := s.BeginAttempt(ctx, r.ID, core.Attempt{MaxMicros: 30})
	if e != nil {
		t.Fatal(e)
	}
	c.Status = "uncertain"
	c.Usage = core.Usage{Credits: 1}
	if e = s.EndAttempt(ctx, c); e != nil {
		t.Fatal(e)
	}
	if e = s.Finish(ctx, r.ID, core.Result{Status: "partial"}); e != nil {
		t.Fatal(e)
	}
	finished, e := s.GetRun(ctx, p.ID, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	u := finished.Result.Usage
	if u.ReportedMicros != 20 || u.EstimatedMicros != 0 || u.ReservedMicros != 30 || u.UnsettledAttempts != 1 || u.Credits != 1 || u.CostKnown {
		t.Fatalf("wrong accounting: %+v", u)
	}
	daily, e := s.Usage(ctx, p.ID)
	if e != nil || daily["budget_consumed_micros"].(int64) != 50 {
		t.Fatalf("wrong settled hold: %v %v", daily, e)
	}
	_, e = s.BeginAttempt(ctx, r.ID, core.Attempt{MaxMicros: 10})
	requireCode(t, e, "cancelled")
}

func TestPriorDayUnknownReservationStillCounts(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p := person(t, s, "alice", 100)
	r := submit(t, s, p, "old", 100)
	if _, e := s.db.Exec(`UPDATE ledger SET accepted_day='2000-01-01' WHERE run_id=?`, r.ID); e != nil {
		t.Fatal(e)
	}
	_, _, e := s.Submit(ctx, p, "new", core.Request{Operation: "search"}, core.Plan{BudgetUSD: core.USD(1)}, 1000, 100)
	requireCode(t, e, "budget_exceeded")
	if e = s.Cancel(ctx, p.ID, r.ID); e != nil {
		t.Fatal(e)
	}
	_, _, e = s.Submit(ctx, p, "new", core.Request{Operation: "search"}, core.Plan{BudgetUSD: core.USD(100)}, 1000, 100)
	if e != nil {
		t.Fatal(e)
	}
}

func TestRecoveryPreservesEvidenceWithoutReplay(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p := person(t, s, "alice", 1000)
	r := submit(t, s, p, "q", 100)
	claim(t, s)
	a, e := s.BeginAttempt(ctx, r.ID, core.Attempt{Provider: "exa", Operation: "search", MaxMicros: 40})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Checkpoint(ctx, r.ID, core.Result{Results: []core.Source{{URL: "https://example.com/evidence", Content: "retained evidence"}}}); e != nil {
		t.Fatal(e)
	}
	path := s.path
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	restored, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	if e = restored.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	got, e := restored.GetRun(ctx, p.ID, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	if got.Status != "partial" || len(got.Result.Results) != 1 || got.Result.Results[0].Content != "retained evidence" {
		t.Fatalf("lost recovery evidence: %+v", got)
	}
	if len(got.Result.Attempts) != 1 || got.Result.Attempts[0].ID != a.ID || got.Result.Attempts[0].Status != "uncertain" || got.Result.Usage.ReservedMicros != 40 {
		t.Fatalf("wrong recovery accounting: %+v", got.Result)
	}
	if next, e := restored.Claim(ctx); e != nil || next != nil {
		t.Fatalf("replayed interrupted work: %v %v", next, e)
	}
	if e = restored.Recover(ctx); e != nil {
		t.Fatal(e)
	}
}

func TestCacheTTLAndPrincipalIsolation(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a := person(t, s, "alice", 1000)
	b := person(t, s, "bob", 1000)
	out := core.Output{Sources: []core.Source{{URL: "https://example.com", Content: "private"}}}
	if e := s.CachePut(ctx, a.ID, "shared-key", out, time.Minute); e != nil {
		t.Fatal(e)
	}
	got, e := s.CacheGet(ctx, a.ID, "shared-key")
	if e != nil || got == nil || got.Sources[0].Content != "private" {
		t.Fatalf("cache failed: %v %v", got, e)
	}
	got, e = s.CacheGet(ctx, b.ID, "shared-key")
	if e != nil || got != nil {
		t.Fatal("cache crossed principal boundary")
	}
	if _, e = s.db.Exec(`UPDATE cache SET expires_at=?`, time.Now().Add(-time.Second).UnixNano()); e != nil {
		t.Fatal(e)
	}
	got, e = s.CacheGet(ctx, a.ID, "shared-key")
	if e != nil || got != nil {
		t.Fatal("expired cache still available")
	}
}

func TestBackupIncludesWALAndDoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p := person(t, s, "alice", 1000)
	r := submit(t, s, p, "backup", 100)
	path := filepath.Join(t.TempDir(), "private", "backup.db")
	if e := s.Backup(ctx, path); e != nil {
		t.Fatal(e)
	}
	if e := s.Backup(ctx, path); e == nil {
		t.Fatal("overwrote backup")
	}
	copy, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer copy.Close()
	got, e := copy.GetRun(ctx, p.ID, r.ID)
	if e != nil || got.ID != r.ID {
		t.Fatalf("WAL task missing: %v %v", got, e)
	}
	var integrity string
	if e = copy.db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); e != nil || integrity != "ok" {
		t.Fatalf("backup integrity: %s %v", integrity, e)
	}
}

func TestSnapshotVerificationRejectsCorruptionReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.db")
	contents := []byte("this is a damaged SQLite snapshot")
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkSnapshot(context.Background(), path); err == nil {
		t.Fatal("integrity verification accepted a corrupt backup")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(contents) {
		t.Fatalf("read-only verification modified the snapshot: %q, %v", got, err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("verification created SQLite sidecar %s: %v", suffix, err)
		}
	}
}

func TestFailedBackupRemovesOnlyNewSnapshot(t *testing.T) {
	s := openTest(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "private", "failed.db")
	if err := s.Backup(context.Background(), path); err == nil {
		t.Fatal("backup succeeded with a closed database")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed snapshot remained: %v", err)
	}
	if _, err := os.Stat(s.path); err != nil {
		t.Fatalf("source database was removed: %v", err)
	}
}

func TestRetentionExpiresPayloadKeepsUnknownAccounting(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p := person(t, s, "alice", 1000)
	r := submit(t, s, p, "sensitive-query", 100)
	claim(t, s)
	a, e := s.BeginAttempt(ctx, r.ID, core.Attempt{MaxMicros: 40})
	if e != nil {
		t.Fatal(e)
	}
	a.Status = "uncertain"
	a.Usage = core.Usage{}
	if e = s.EndAttempt(ctx, a); e != nil {
		t.Fatal(e)
	}
	if e = s.Finish(ctx, r.ID, core.Result{Status: "failed"}); e != nil {
		t.Fatal(e)
	}
	old := time.Now().Add(-2 * 24 * time.Hour).UnixNano()
	if _, e = s.db.Exec(`UPDATE runs SET updated_at=? WHERE id=?`, old, r.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.Cleanup(ctx, 1); e != nil {
		t.Fatal(e)
	}
	_, e = s.GetRun(ctx, p.ID, r.ID)
	requireCode(t, e, "gone")
	_, existing, e := s.Submit(ctx, p, "sensitive-query", r.Request, r.Plan, 1000, 100)
	requireCode(t, e, "gone")
	if !existing {
		t.Fatal("expired result lost idempotency")
	}
	var req string
	if e = s.db.QueryRow(`SELECT request FROM runs WHERE id=?`, r.ID).Scan(&req); e != nil || req != "{}" {
		t.Fatal("retained task query")
	}
	u, e := s.Usage(ctx, p.ID)
	if e != nil || u["reserved_micros"].(int64) != 40 {
		t.Fatalf("unknown charge disappeared: %v %v", u, e)
	}
}

func TestConcurrentStoresDoNotOversubscribe(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	other, e := Open(s.path)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	p := person(t, s, "alice", 100)
	var wg sync.WaitGroup
	var accepted atomic.Int64
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db := s
			if i%2 == 1 {
				db = other
			}
			_, _, e := db.Submit(ctx, p, strings.Repeat("k", i+1), core.Request{Operation: "search"}, core.Plan{BudgetUSD: core.USD(100)}, 1000, 100)
			if e == nil {
				accepted.Add(1)
			} else {
				var ce *core.Error
				if !errors.As(e, &ce) || ce.Code != "budget_exceeded" {
					t.Errorf("concurrent store failed: %v", e)
				}
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted %d over budget", accepted.Load())
	}
}

func TestCancelRunningConservativelyFinalizesUnfinishedCall(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p := person(t, s, "alice", 1000)
	r := submit(t, s, p, "cancel", 100)
	claim(t, s)
	if _, e := s.BeginAttempt(ctx, r.ID, core.Attempt{MaxMicros: 60}); e != nil {
		t.Fatal(e)
	}
	if e := s.Cancel(ctx, p.ID, r.ID); e != nil {
		t.Fatal(e)
	}
	if !s.Cancelled(ctx, r.ID) {
		t.Fatal("cancellation not visible")
	}
	if e := s.Finish(ctx, r.ID, core.Result{Status: "cancelled"}); e != nil {
		t.Fatal(e)
	}
	got, e := s.GetRun(ctx, p.ID, r.ID)
	if e != nil || got.Status != "cancelled" || got.Result.Attempts[0].Status != "uncertain" || got.Result.Usage.ReservedMicros != 60 {
		t.Fatalf("lost in-flight reservation: %+v, %v", got, e)
	}
}

func TestClaimExpiresQueuedDeadline(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p := person(t, s, "alice", 1000)
	r := submit(t, s, p, "expired", 100)
	if _, e := s.db.Exec(`UPDATE runs SET deadline=? WHERE id=?`, time.Now().Add(-time.Second).UnixNano(), r.ID); e != nil {
		t.Fatal(e)
	}
	got, e := s.Claim(ctx)
	if e != nil || got != nil {
		t.Fatalf("expired task claimed: %v %v", got, e)
	}
	finished, e := s.GetRun(ctx, p.ID, r.ID)
	if e != nil || finished.Status != "failed" || finished.Result.StopReason != "deadline_exceeded" {
		t.Fatalf("expired task did not finish: %v %v", finished, e)
	}
	u, e := s.Usage(ctx, p.ID)
	if e != nil || u["reserved_micros"].(int64) != 0 {
		t.Fatalf("queued reservation not released: %v %v", u, e)
	}
}

func TestOpenDoesNotChangeSharedDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	if e := os.Mkdir(dir, 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(dir, 0755); e != nil {
		t.Fatal(e)
	}
	if s, e := Open(filepath.Join(dir, "state.db")); e == nil {
		s.Close()
		t.Fatal("accepted unsafe state directory")
	}
	info, e := os.Stat(dir)
	if e != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("modified shared directory: %v %v", info, e)
	}
}

func TestAdministrativeSettlementPreservesOtherReservations(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p := person(t, s, "alice", 1000)
	r := submit(t, s, p, "settle", 100)
	claim(t, s)
	a, e := s.BeginAttempt(ctx, r.ID, core.Attempt{MaxMicros: 60})
	if e != nil {
		t.Fatal(e)
	}
	a.Status = "uncertain"
	a.Usage = core.Usage{}
	if e = s.EndAttempt(ctx, a); e != nil {
		t.Fatal(e)
	}
	if e = s.Finish(ctx, r.ID, core.Result{Status: "failed"}); e != nil {
		t.Fatal(e)
	}
	submit(t, s, p, "still-reserved", 100)
	if e = s.SettleAttempt(ctx, a.ID, core.Usage{CostKnown: true, ReportedMicros: 20}); e != nil {
		t.Fatal(e)
	}
	u, e := s.Usage(ctx, p.ID)
	if e != nil || u["reported_micros"].(int64) != 20 || u["reserved_micros"].(int64) != 100 {
		t.Fatalf("settlement changed other task reservation: %v %v", u, e)
	}
	if e = s.SettleAttempt(ctx, a.ID, core.Usage{CostKnown: true, ReportedMicros: 20}); e != nil {
		t.Fatal(e)
	}
	e = s.SettleAttempt(ctx, a.ID, core.Usage{CostKnown: true, ReportedMicros: 21})
	requireCode(t, e, "settlement_conflict")
	got, e := s.GetRun(ctx, p.ID, r.ID)
	if e != nil || got.Result.Usage.ReportedMicros != 20 || got.Result.Usage.ReservedMicros != 0 {
		t.Fatalf("persisted result did not reconcile: %v %v", got, e)
	}
	events, e := s.Events(ctx, p.ID, r.ID, 0)
	if e != nil {
		t.Fatal(e)
	}
	count := 0
	for _, v := range events {
		if v.Type == "settlement" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("wanted one immutable settlement event, got %d", count)
	}
}

func TestSettlementWorstCaseAndKnownZero(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p := person(t, s, "alice", 1000)
	r := submit(t, s, p, "q", 200)
	claim(t, s)
	a, e := s.BeginAttempt(ctx, r.ID, core.Attempt{MaxMicros: 40})
	if e != nil {
		t.Fatal(e)
	}
	a.Status = "failed"
	a.Usage = core.Usage{}
	if e = s.EndAttempt(ctx, a); e != nil {
		t.Fatal(e)
	}
	b, e := s.BeginAttempt(ctx, r.ID, core.Attempt{MaxMicros: 50})
	if e != nil {
		t.Fatal(e)
	}
	b.Status = "failed"
	b.Usage = core.Usage{}
	if e = s.EndAttempt(ctx, b); e != nil {
		t.Fatal(e)
	}
	e = s.SettleAttempt(ctx, a.ID, core.Usage{EstimatedMicros: 20})
	requireCode(t, e, "invalid_usage")
	if e = s.SettleAttempt(ctx, a.ID, core.Usage{EstimatedMicros: 40}); e != nil {
		t.Fatal(e)
	}
	u, e := s.Usage(ctx, p.ID)
	if e != nil || u["reserved_micros"].(int64) != 200 {
		t.Fatalf("active run reservation prematurely released: %v %v", u, e)
	}
	if e = s.Finish(ctx, r.ID, core.Result{Status: "failed"}); e != nil {
		t.Fatal(e)
	}
	if e = s.SettleAttempt(ctx, b.ID, core.Usage{CostKnown: true}); e != nil {
		t.Fatal(e)
	}
	u, e = s.Usage(ctx, p.ID)
	if e != nil || u["estimated_micros"].(int64) != 40 || u["reserved_micros"].(int64) != 0 {
		t.Fatalf("wrong worst-case/zero settlement: %v %v", u, e)
	}
	got, e := s.GetAttempt(ctx, b.ID)
	if e != nil || !got.Usage.CostKnown {
		t.Fatalf("known zero missing: %v %v", got, e)
	}
}

func TestIdempotentLookupIndependentOfNewPlan(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p := person(t, s, "alice", 1000)
	req := core.Request{Operation: "search", Query: "q"}
	plan := core.Plan{Request: core.Request{Operation: "search", Query: "q", Limit: 5, DeadlineSeconds: 500}, BudgetUSD: core.USD(100)}
	r, _, e := s.Submit(ctx, p, "stable-key", req, plan, 1000, 100)
	if e != nil {
		t.Fatal(e)
	}
	got, exists, e := s.LookupIdempotent(ctx, p.ID, "stable-key", req)
	if e != nil || !exists || got.ID != r.ID {
		t.Fatalf("lookup: %v %v %v", got, exists, e)
	}
	if r.Deadline.Sub(r.CreatedAt) != 500*time.Second {
		t.Fatal("normalized deadline was not used")
	}
	got, exists, e = s.Submit(ctx, p, "stable-key", req, core.Plan{}, 0, 0)
	if e != nil || !exists || got.ID != r.ID {
		t.Fatalf("new plan changed accepted task: %v %v %v", got, exists, e)
	}
	req.Query = "changed"
	_, _, e = s.LookupIdempotent(ctx, p.ID, "stable-key", req)
	requireCode(t, e, "idempotency_conflict")
}
