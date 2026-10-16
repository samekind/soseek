package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jstar0/soseek/internal/core"
)

const runColumns = `id,principal_id,status,request,plan,principal,revision,budget,created_at,updated_at,deadline,cancel_requested,result,error,expired`

type scanner interface{ Scan(...any) error }

func scanRun(row scanner) (core.Run, error) {
	var r core.Run
	var req, plan, p string
	var result, runErr sql.NullString
	var created, updated, deadline int64
	var expired bool
	e := row.Scan(&r.ID, &r.PrincipalID, &r.Status, &req, &plan, &p, &r.Revision, &r.BudgetMicros, &created, &updated, &deadline, &r.CancelRequested, &result, &runErr, &expired)
	if e != nil {
		return r, notFound(e)
	}
	if expired {
		return r, core.E("gone", "task content has expired")
	}
	r.CreatedAt = stamp(created)
	r.UpdatedAt = stamp(updated)
	r.Deadline = stamp(deadline)
	for _, v := range []struct {
		s  string
		to any
	}{{req, &r.Request}, {plan, &r.Plan}, {p, &r.Principal}} {
		if e = json.Unmarshal([]byte(v.s), v.to); e != nil {
			return r, e
		}
	}
	if result.Valid {
		if e = json.Unmarshal([]byte(result.String), &r.Result); e != nil {
			return r, e
		}
	}
	if runErr.Valid {
		if e = json.Unmarshal([]byte(runErr.String), &r.Error); e != nil {
			return r, e
		}
	}
	return r, nil
}
func getRun(ctx context.Context, q querier, id string) (core.Run, error) {
	return scanRun(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id=?`, id))
}
func (s *Store) GetRun(ctx context.Context, principalID, id string) (core.Run, error) {
	return scanRun(s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id=? AND principal_id=?`, id, principalID))
}

// LookupIdempotent lets the API recover an already accepted request before
// consulting possibly changed routing, defaults, permissions or budgets. Submit
// repeats this check inside its transaction to close the lookup/create race.
func (s *Store) LookupIdempotent(ctx context.Context, principalID, key string, req core.Request) (core.Run, bool, error) {
	request, err := marshal(req)
	if err != nil {
		return core.Run{}, false, err
	}
	var id, digest string
	err = s.db.QueryRowContext(ctx, `SELECT run_id,request_hash FROM idempotency WHERE principal_id=? AND key=?`, principalID, key).Scan(&id, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Run{}, false, nil
	}
	if err != nil {
		return core.Run{}, false, err
	}
	if digest != hash(request) {
		return core.Run{}, false, core.E("idempotency_conflict", "idempotency key already belongs to a different request")
	}
	r, err := s.GetRun(ctx, principalID, id)
	return r, true, err
}

// Submit atomically persists the task, principal-scoped idempotency record and
// entire run reservation. Only after this commits may HTTP return acceptance.
func (s *Store) Submit(ctx context.Context, p core.Principal, key string, req core.Request, plan core.Plan, globalDailyMicros int64, queueLimit int) (core.Run, bool, error) {
	var empty core.Run
	if key == "" || len(key) > 256 {
		return empty, false, core.E("invalid_request", "idempotency key must contain 1 to 256 bytes")
	}
	budget, budgetErr := core.ParseUSD(plan.BudgetUSD)
	request, e := marshal(req)
	if e != nil {
		return empty, false, e
	}
	pj, e := marshal(plan)
	if e != nil {
		return empty, false, e
	}
	principal, e := marshal(p)
	if e != nil {
		return empty, false, e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return empty, false, e
	}
	defer tx.Rollback()
	var oldID, oldHash string
	e = tx.QueryRowContext(ctx, `SELECT run_id,request_hash FROM idempotency WHERE principal_id=? AND key=?`, p.ID, key).Scan(&oldID, &oldHash)
	if e == nil {
		if oldHash != hash(request) {
			return empty, false, core.E("idempotency_conflict", "idempotency key already belongs to a different request")
		}
		r, e := getRun(ctx, tx, oldID)
		return r, true, e
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return empty, false, e
	}
	if budgetErr != nil || budget <= 0 || globalDailyMicros <= 0 || queueLimit <= 0 {
		return empty, false, core.E("invalid_request", "positive task, deployment and queue limits required")
	}
	var daily int64
	var revoked bool
	e = tx.QueryRowContext(ctx, `SELECT daily_micros,revoked FROM principals WHERE id=?`, p.ID).Scan(&daily, &revoked)
	if errors.Is(e, sql.ErrNoRows) || revoked {
		return empty, false, core.E("unauthorized", "principal is not active")
	}
	if e != nil {
		return empty, false, e
	}
	var queued int
	e = tx.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE status='queued'`).Scan(&queued)
	if e != nil {
		return empty, false, e
	}
	if queued >= queueLimit {
		return empty, false, core.E("queue_full", "task queue is full")
	}
	accepted := time.Now().UTC()
	day := accepted.Format("2006-01-02")
	// Current-day settled costs plus every day's outstanding reservation. This
	// keeps old unknown attempts from silently disappearing at UTC midnight.
	var personal, total int64
	e = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN accepted_day=? THEN reported+estimated ELSE 0 END + reserved),0) FROM ledger WHERE principal_id=?`, day, p.ID).Scan(&personal)
	if e != nil {
		return empty, false, e
	}
	e = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN accepted_day=? THEN reported+estimated ELSE 0 END + reserved),0) FROM ledger`, day).Scan(&total)
	if e != nil {
		return empty, false, e
	}
	if budget > daily-personal {
		return empty, false, core.E("budget_exceeded", "principal daily budget would be exceeded")
	}
	if budget > globalDailyMicros-total {
		return empty, false, core.E("budget_exceeded", "deployment daily budget would be exceeded")
	}
	id, e := randomID("run_")
	if e != nil {
		return empty, false, e
	}
	seconds := req.DeadlineSeconds
	if plan.Request.DeadlineSeconds > 0 {
		seconds = plan.Request.DeadlineSeconds
	}
	if seconds <= 0 {
		seconds = 120
	}
	r := core.Run{ID: id, PrincipalID: p.ID, Principal: p, Status: "queued", Request: req, Plan: plan, Revision: plan.Revision, BudgetMicros: budget, CreatedAt: accepted, UpdatedAt: accepted, Deadline: accepted.Add(time.Duration(seconds) * time.Second)}
	_, e = tx.ExecContext(ctx, `INSERT INTO runs(id,principal_id,status,request,plan,principal,revision,budget,created_at,updated_at,deadline) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, p.ID, r.Status, request, pj, principal, r.Revision, budget, accepted.UnixNano(), accepted.UnixNano(), r.Deadline.UnixNano())
	if e != nil {
		return empty, false, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO idempotency(principal_id,key,request_hash,run_id) VALUES(?,?,?,?)`, p.ID, key, hash(request), id)
	if e != nil {
		return empty, false, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO ledger(run_id,principal_id,accepted_day,reserved,updated_at) VALUES(?,?,?,?,?)`, id, p.ID, day, budget, now())
	if e != nil {
		return empty, false, e
	}
	if e = event(ctx, tx, id, "accepted", map[string]any{"run_id": id, "status": "queued"}); e != nil {
		return empty, false, e
	}
	if e = tx.Commit(); e != nil {
		return empty, false, e
	}
	return r, false, nil
}

// Claim uses least-recently-served identity scheduling and at most one active
// task per identity. Expired queued tasks are finalized without contacting upstream.
func (s *Store) Claim(ctx context.Context) (*core.Run, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	for {
		var id string
		e = tx.QueryRowContext(ctx, `SELECT r.id FROM runs r JOIN principals p ON p.id=r.principal_id WHERE r.status='queued' AND p.revoked=0 AND NOT EXISTS(SELECT 1 FROM runs active WHERE active.principal_id=r.principal_id AND active.status='running') ORDER BY p.last_claimed,r.created_at,r.id LIMIT 1`).Scan(&id)
		if errors.Is(e, sql.ErrNoRows) {
			if e = tx.Commit(); e != nil {
				return nil, e
			}
			return nil, nil
		}
		if e != nil {
			return nil, e
		}
		r, e := getRun(ctx, tx, id)
		if e != nil {
			return nil, e
		}
		if !time.Now().Before(r.Deadline) {
			result := core.Result{SchemaVersion: core.SchemaVersion, RunID: id, Status: "failed", Operation: r.Request.Operation, StopReason: "deadline_exceeded", Results: []core.Source{}, Attempts: []core.Attempt{}, Warnings: []string{"Task deadline elapsed while queued."}}
			if e = finishTx(ctx, tx, id, result); e != nil {
				return nil, e
			}
			continue
		}
		t := now()
		_, e = tx.ExecContext(ctx, `UPDATE runs SET status='running',updated_at=? WHERE id=?`, t, id)
		if e != nil {
			return nil, e
		}
		_, e = tx.ExecContext(ctx, `UPDATE principals SET last_claimed=? WHERE id=?`, t, r.PrincipalID)
		if e != nil {
			return nil, e
		}
		if e = event(ctx, tx, id, "running", map[string]string{"status": "running"}); e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
		r.Status = "running"
		r.UpdatedAt = stamp(t)
		return &r, nil
	}
}

func (s *Store) Finish(ctx context.Context, id string, result core.Result) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = finishTx(ctx, tx, id, result); e != nil {
		return e
	}
	return tx.Commit()
}
func finishTx(ctx context.Context, tx *sql.Tx, id string, result core.Result) error {
	r, e := getRun(ctx, tx, id)
	if e != nil {
		return e
	}
	if core.Terminal(r.Status) {
		return nil
	}
	if !core.Terminal(result.Status) {
		return core.E("invalid_request", "result status must be terminal")
	}
	attempts, e := attemptsQuery(ctx, tx, id)
	if e != nil {
		return e
	}
	for i := range attempts {
		a := &attempts[i]
		if a.Status != "in_flight" {
			continue
		}
		// Cancellation or an executor failure must not leave a terminal run
		// with a misleadingly live attempt or release an unknown charge.
		a.Status = "uncertain"
		a.Error = &core.Error{Code: "outcome_unknown", Message: "Task ended before the upstream outcome was recorded; call was not replayed.", Uncertain: true}
		t := time.Now().UTC()
		a.FinishedAt = &t
		a.Usage = core.Usage{ReservedMicros: a.MaxMicros, UnsettledAttempts: 1}
		j, err := marshal(a)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE attempts SET status='uncertain',reported=0,estimated=0,reserved=?,data=? WHERE id=?`, a.MaxMicros, j, a.ID); err != nil {
			return err
		}
	}
	result.RunID = id
	result.SchemaVersion = core.SchemaVersion
	result.Attempts = attempts
	result.Usage = sumUsage(attempts)
	if result.Operation == "" {
		result.Operation = r.Request.Operation
	}
	if result.Results == nil {
		result.Results = []core.Source{}
	}
	j, e := marshal(result)
	if e != nil {
		return e
	}
	t := now()
	_, e = tx.ExecContext(ctx, `UPDATE runs SET status=?,result=?,updated_at=? WHERE id=?`, result.Status, j, t, id)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE ledger SET reported=?,estimated=?,reserved=?,unsettled=?,updated_at=? WHERE run_id=?`, result.Usage.ReportedMicros, result.Usage.EstimatedMicros, result.Usage.ReservedMicros, result.Usage.UnsettledAttempts, t, id)
	if e != nil {
		return e
	}
	return event(ctx, tx, id, "completed", map[string]string{"status": result.Status, "stop_reason": result.StopReason})
}
func (s *Store) Cancel(ctx context.Context, principalID, id string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	r, e := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id=? AND principal_id=?`, id, principalID))
	if e != nil {
		return e
	}
	if core.Terminal(r.Status) {
		return nil
	}
	if r.CancelRequested {
		return nil
	}
	_, e = tx.ExecContext(ctx, `UPDATE runs SET cancel_requested=1,updated_at=? WHERE id=?`, now(), id)
	if e != nil {
		return e
	}
	if r.Status == "queued" {
		e = finishTx(ctx, tx, id, core.Result{Status: "cancelled", StopReason: "cancelled"})
	} else {
		e = event(ctx, tx, id, "cancel_requested", map[string]bool{"cancel_requested": true})
	}
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Cancelled(ctx context.Context, id string) bool {
	var cancelled bool
	var status string
	e := s.db.QueryRowContext(ctx, `SELECT cancel_requested,status FROM runs WHERE id=?`, id).Scan(&cancelled, &status)
	return e != nil || cancelled || core.Terminal(status)
}

func (s *Store) Events(ctx context.Context, principalID, id string, after int64) ([]core.Event, error) {
	if _, e := s.GetRun(ctx, principalID, id); e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(ctx, `SELECT id,kind,data,created_at FROM events WHERE run_id=? AND id>? ORDER BY id LIMIT 500`, id, after)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	events := []core.Event{}
	for rows.Next() {
		var v core.Event
		var data string
		var t int64
		v.RunID = id
		if e = rows.Scan(&v.ID, &v.Type, &data, &t); e != nil {
			return nil, e
		}
		v.Data = json.RawMessage(data)
		v.CreatedAt = stamp(t)
		events = append(events, v)
	}
	return events, rows.Err()
}
func (s *Store) AddEvent(ctx context.Context, id, kind string, data any) error {
	return event(ctx, s.db, id, kind, data)
}
func (s *Store) Checkpoint(ctx context.Context, id string, data any) error {
	j, e := marshal(data)
	if e != nil {
		return e
	}
	r, e := s.db.ExecContext(ctx, `UPDATE runs SET checkpoint=?,updated_at=? WHERE id=? AND status='running'`, j, now(), id)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return core.E("not_found", "running task not found")
	}
	return nil
}

func (s *Store) Usage(ctx context.Context, principalID string) (map[string]any, error) {
	day := time.Now().UTC().Format("2006-01-02")
	var reported, estimated, reserved, unsettled, held int64
	e := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN accepted_day=? THEN reported ELSE 0 END),0), COALESCE(SUM(CASE WHEN accepted_day=? THEN estimated ELSE 0 END),0), COALESCE(SUM(reserved),0),COALESCE(SUM(unsettled),0),COALESCE(SUM(CASE WHEN accepted_day!=? THEN reserved ELSE 0 END),0) FROM ledger WHERE principal_id=?`, day, day, day, principalID).Scan(&reported, &estimated, &reserved, &unsettled, &held)
	if e != nil {
		return nil, e
	}
	return map[string]any{"utc_day": day, "reported_micros": reported, "estimated_micros": estimated, "reserved_micros": reserved, "unsettled_attempts": unsettled, "prior_day_reserved_micros": held, "budget_consumed_micros": reported + estimated + reserved}, nil
}
