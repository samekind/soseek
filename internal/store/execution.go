package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/jstar0/soseek/internal/core"
)

func (s *Store) BeginAttempt(ctx context.Context, runID string, a core.Attempt) (core.Attempt, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return a, e
	}
	defer tx.Rollback()
	r, e := getRun(ctx, tx, runID)
	if e != nil {
		return a, e
	}
	if r.Status != "running" || r.CancelRequested {
		return a, core.E("cancelled", "task is no longer accepting upstream calls")
	}
	if !time.Now().Before(r.Deadline) {
		return a, core.E("deadline_exceeded", "task deadline has elapsed")
	}
	if a.MaxMicros < 0 || (!a.Cached && a.MaxMicros == 0) {
		return a, core.E("cost_bound_unavailable", "a positive per-call cost bound is required")
	}
	var used int64
	e = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(reported+estimated+reserved),0) FROM attempts WHERE run_id=?`, runID).Scan(&used)
	if e != nil {
		return a, e
	}
	if a.MaxMicros > r.BudgetMicros-used {
		return a, core.E("budget_exceeded", "task budget cannot reserve another upstream call")
	}
	if a.ID == "" {
		a.ID, e = randomID("att_")
		if e != nil {
			return a, e
		}
	}
	var exists int
	e = tx.QueryRowContext(ctx, `SELECT count(*) FROM attempts WHERE id=?`, a.ID).Scan(&exists)
	if e != nil {
		return a, e
	}
	if exists > 0 {
		return a, core.E("attempt_exists", "attempt already exists and must not be replayed")
	}
	a.RunID = runID
	a.Status = "in_flight"
	a.StartedAt = time.Now().UTC()
	a.FinishedAt = nil
	a.Error = nil
	a.Usage = core.Usage{ReservedMicros: a.MaxMicros, UnsettledAttempts: 1}
	if a.Cached {
		a.Usage = core.Usage{CostKnown: true}
	}
	j, e := marshal(a)
	if e != nil {
		return a, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO attempts(id,run_id,status,reserved,data) VALUES(?,?,?,?,?)`, a.ID, runID, a.Status, a.Usage.ReservedMicros, j)
	if e != nil {
		return a, e
	}
	if e = event(ctx, tx, runID, "attempt_started", map[string]any{"attempt_id": a.ID, "provider": a.Provider, "channel": a.Channel, "operation": a.Operation, "reserved_micros": a.Usage.ReservedMicros}); e != nil {
		return a, e
	}
	return a, tx.Commit()
}

func normalizeUsage(a *core.Attempt) error {
	u := &a.Usage
	if u.ReportedMicros < 0 || u.EstimatedMicros < 0 || u.ReservedMicros < 0 || u.InputTokens < 0 || u.OutputTokens < 0 || u.NativeCalls < 0 || u.Credits < 0 {
		return core.E("invalid_usage", "usage values cannot be negative")
	}
	if a.Cached {
		*u = core.Usage{CostKnown: true}
		return nil
	}
	// A reported charge supersedes an estimate rather than double charging it.
	if u.ReportedMicros > 0 || u.CostKnown {
		u.EstimatedMicros = 0
		u.ReservedMicros = 0
		u.UnsettledAttempts = 0
		u.CostKnown = true
		return nil
	}
	if u.EstimatedMicros > 0 {
		u.ReservedMicros = 0
		u.UnsettledAttempts = 0
		return nil
	}
	u.ReservedMicros = a.MaxMicros
	u.UnsettledAttempts = 1
	return nil
}
func (s *Store) EndAttempt(ctx context.Context, a core.Attempt) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var j string
	e = tx.QueryRowContext(ctx, `SELECT data FROM attempts WHERE id=?`, a.ID).Scan(&j)
	if e != nil {
		return notFound(e)
	}
	var old core.Attempt
	if e = json.Unmarshal([]byte(j), &old); e != nil {
		return e
	}
	if old.Status != "in_flight" {
		return nil
	}
	// Identity and original ceiling are immutable; callers may only settle outcome.
	a.RunID = old.RunID
	a.Provider = old.Provider
	a.Channel = old.Channel
	a.Operation = old.Operation
	a.MaxMicros = old.MaxMicros
	a.StartedAt = old.StartedAt
	a.Cached = old.Cached
	if a.Status == "" || a.Status == "in_flight" {
		return core.E("invalid_request", "attempt outcome must be terminal")
	}
	t := time.Now().UTC()
	a.FinishedAt = &t
	if e = normalizeUsage(&a); e != nil {
		return e
	}
	j, e = marshal(a)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE attempts SET status=?,reported=?,estimated=?,reserved=?,data=? WHERE id=?`, a.Status, a.Usage.ReportedMicros, a.Usage.EstimatedMicros, a.Usage.ReservedMicros, j, a.ID)
	if e != nil {
		return e
	}
	if e = event(ctx, tx, a.RunID, "attempt_completed", map[string]any{"attempt_id": a.ID, "status": a.Status, "usage": a.Usage}); e != nil {
		return e
	}
	return tx.Commit()
}

func attemptsQuery(ctx context.Context, q querier, runID string) ([]core.Attempt, error) {
	rows, e := q.QueryContext(ctx, `SELECT data FROM attempts WHERE run_id=? ORDER BY rowid`, runID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []core.Attempt{}
	for rows.Next() {
		var j string
		var a core.Attempt
		if e = rows.Scan(&j); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(j), &a); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) Attempts(ctx context.Context, runID string) ([]core.Attempt, error) {
	return attemptsQuery(ctx, s.db, runID)
}
func sumUsage(attempts []core.Attempt) core.Usage {
	u := core.Usage{CostKnown: true}
	for _, a := range attempts {
		u.ReportedMicros += a.Usage.ReportedMicros
		u.EstimatedMicros += a.Usage.EstimatedMicros
		u.ReservedMicros += a.Usage.ReservedMicros
		u.UnsettledAttempts += a.Usage.UnsettledAttempts
		u.InputTokens += a.Usage.InputTokens
		u.OutputTokens += a.Usage.OutputTokens
		u.NativeCalls += a.Usage.NativeCalls
		u.Credits += a.Usage.Credits
		u.CostKnown = u.CostKnown && a.Usage.CostKnown
	}
	return u
}

func (s *Store) CacheGet(ctx context.Context, principalID, key string) (*core.Output, error) {
	var j string
	e := s.db.QueryRowContext(ctx, `SELECT data FROM cache WHERE principal_id=? AND key=? AND expires_at>?`, principalID, key, now()).Scan(&j)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var out core.Output
	if e = json.Unmarshal([]byte(j), &out); e != nil {
		return nil, e
	}
	return &out, nil
}
func (s *Store) CachePut(ctx context.Context, principalID, key string, out core.Output, ttl time.Duration) error {
	if ttl <= 0 {
		return nil
	}
	j, e := marshal(out)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO cache(principal_id,key,data,expires_at) VALUES(?,?,?,?) ON CONFLICT(principal_id,key) DO UPDATE SET data=excluded.data,expires_at=excluded.expires_at`, principalID, key, j, time.Now().Add(ttl).UnixNano())
	return e
}

// Recover runs once before worker startup. It never replays running work. Any
// persisted in-flight attempt remains fully reserved and explicitly uncertain.
func (s *Store) Recover(ctx context.Context) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT id,checkpoint FROM runs WHERE status='running'`)
	if e != nil {
		return e
	}
	type pending struct {
		id         string
		checkpoint sql.NullString
	}
	var pendingRuns []pending
	for rows.Next() {
		var p pending
		if e = rows.Scan(&p.id, &p.checkpoint); e != nil {
			rows.Close()
			return e
		}
		pendingRuns = append(pendingRuns, p)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, p := range pendingRuns {
		as, e := attemptsQuery(ctx, tx, p.id)
		if e != nil {
			return e
		}
		for _, a := range as {
			if a.Status != "in_flight" {
				continue
			}
			a.Status = "uncertain"
			a.Error = &core.Error{Code: "interrupted", Message: "Service stopped before upstream outcome was durably recorded; call was not replayed.", Uncertain: true}
			t := time.Now().UTC()
			a.FinishedAt = &t
			a.Usage = core.Usage{ReservedMicros: a.MaxMicros, UnsettledAttempts: 1}
			j, e := marshal(a)
			if e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, `UPDATE attempts SET status='uncertain',reported=0,estimated=0,reserved=?,data=? WHERE id=?`, a.MaxMicros, j, a.ID)
			if e != nil {
				return e
			}
		}
		result := core.Result{Status: "failed", StopReason: "service_interrupted"}
		if p.checkpoint.Valid {
			// The executor writes core.Result. Accept a wrapped result as well for
			// forward-compatible checkpoint envelopes, without replaying actions.
			if e = json.Unmarshal([]byte(p.checkpoint.String), &result); e != nil {
				return fmt.Errorf("decode checkpoint: %w", e)
			}
			if len(result.Results) == 0 && result.Answer == "" {
				var envelope struct {
					Result *core.Result `json:"result"`
				}
				if e = json.Unmarshal([]byte(p.checkpoint.String), &envelope); e == nil && envelope.Result != nil {
					result = *envelope.Result
				}
			}
		}
		result.Status = "failed"
		if len(result.Results) > 0 || result.Answer != "" {
			result.Status = "partial"
		}
		result.StopReason = "service_interrupted"
		result.Warnings = append(result.Warnings, "Service restarted. In-flight calls were not replayed; any unknown charge remains reserved.")
		if e = finishTx(ctx, tx, p.id, result); e != nil {
			return e
		}
	}
	return tx.Commit()
}

// Cleanup expires private payloads independently of the minimal accounting
// ledger. Unknown reservations have no automatic expiry and keep consuming risk.
func (s *Store) Cleanup(ctx context.Context, days int) error {
	if days <= 0 {
		return core.E("invalid_request", "retention days must be positive")
	}
	t := time.Now().UTC()
	cutoff := t.Add(-time.Duration(days) * 24 * time.Hour).UnixNano()
	idempotencyCutoff := t.Add(-7 * 24 * time.Hour).UnixNano()
	ledgerCutoff := t.Add(-90 * 24 * time.Hour).UnixNano()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	statements := []struct {
		q    string
		args []any
	}{
		{`DELETE FROM cache WHERE expires_at<=?`, []any{t.UnixNano()}},
		{`DELETE FROM events WHERE run_id IN (SELECT id FROM runs WHERE status IN ('succeeded','partial','failed','cancelled') AND updated_at<?)`, []any{cutoff}},
		// Attempts contain accounting and routing metadata, not request bodies.
		// Remove possible provider error text at payload expiry; retain the
		// minimal per-attempt record until ledger expiry for reconciliation.
		{`UPDATE attempts SET data=json_remove(data,'$.error') WHERE run_id IN (SELECT id FROM runs WHERE status IN ('succeeded','partial','failed','cancelled') AND updated_at<?)`, []any{cutoff}},
		{`UPDATE runs SET request='{}',plan='{}',principal='{}',result=NULL,error=NULL,checkpoint=NULL,expired=1 WHERE status IN ('succeeded','partial','failed','cancelled') AND updated_at<?`, []any{cutoff}},
		{`DELETE FROM idempotency WHERE run_id IN (SELECT id FROM runs WHERE status IN ('succeeded','partial','failed','cancelled') AND updated_at<?)`, []any{idempotencyCutoff}},
		{`DELETE FROM settlements WHERE run_id IN (SELECT id FROM runs WHERE expired=1 AND updated_at<? AND id NOT IN (SELECT run_id FROM idempotency) AND id IN (SELECT run_id FROM ledger WHERE reserved=0 AND updated_at<?))`, []any{ledgerCutoff, ledgerCutoff}},
		{`DELETE FROM attempts WHERE run_id IN (SELECT id FROM runs WHERE expired=1 AND updated_at<? AND id NOT IN (SELECT run_id FROM idempotency) AND id IN (SELECT run_id FROM ledger WHERE reserved=0 AND updated_at<?))`, []any{ledgerCutoff, ledgerCutoff}},
		{`DELETE FROM runs WHERE expired=1 AND updated_at<? AND id NOT IN (SELECT run_id FROM idempotency) AND id IN (SELECT run_id FROM ledger WHERE reserved=0 AND updated_at<?)`, []any{ledgerCutoff, ledgerCutoff}},
		{`DELETE FROM ledger WHERE reserved=0 AND updated_at<? AND run_id NOT IN (SELECT id FROM runs)`, []any{ledgerCutoff}},
	}
	for _, v := range statements {
		if _, e = tx.ExecContext(ctx, v.q, v.args...); e != nil {
			return e
		}
	}
	return tx.Commit()
}

// Backup takes a consistent SQLite snapshot with VACUUM INTO, including committed
// WAL data, then verifies the independent read-only snapshot before reporting
// success. It refuses to replace an existing backup. A backup is sensitive data.
func (s *Store) Backup(ctx context.Context, path string) error {
	abs, e := filepath.Abs(path)
	if e != nil {
		return e
	}
	if abs == s.path {
		return core.E("invalid_request", "backup cannot replace the active database")
	}
	if e = privateDir(filepath.Dir(abs)); e != nil {
		return e
	}
	f, e := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(abs)
		}
	}()
	if e = f.Close(); e != nil {
		return e
	}
	if _, e = s.db.ExecContext(ctx, `VACUUM INTO ?`, abs); e != nil {
		return e
	}
	if e = os.Chmod(abs, 0600); e != nil {
		return e
	}
	f, e = os.OpenFile(abs, os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		_ = f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = checkSnapshot(ctx, abs); e != nil {
		return e
	}
	complete = true
	return nil
}

func checkSnapshot(ctx context.Context, path string) error {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "ro")
	// VACUUM INTO produces a complete standalone snapshot; immutable mode also
	// prevents verification from creating sidecar files beside the backup.
	q.Set("immutable", "1")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return fmt.Errorf("open backup for integrity verification: %w", err)
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("backup integrity_check failed: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var result string
		if err = rows.Scan(&result); err != nil {
			return err
		}
		if result != "ok" {
			return fmt.Errorf("backup integrity_check failed: %s", result)
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("backup integrity_check did not return one ok result")
	}
	return nil
}
