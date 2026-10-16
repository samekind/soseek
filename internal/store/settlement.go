package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/samekind/soseek/internal/core"
)

// GetAttempt is an administrative local operation, not an unscoped public API.
func (s *Store) GetAttempt(ctx context.Context, id string) (core.Attempt, error) {
	var a core.Attempt
	var j string
	if err := s.db.QueryRowContext(ctx, `SELECT data FROM attempts WHERE id=?`, id).Scan(&j); err != nil {
		return a, notFound(err)
	}
	err := json.Unmarshal([]byte(j), &a)
	return a, err
}

// SettleAttempt is an explicit administrative reconciliation. It accepts either
// a provider-verified USD charge (including zero) or the original worst-case
// ceiling as an estimate. It cannot silently revise a previous settlement.
func (s *Store) SettleAttempt(ctx context.Context, id string, usage core.Usage) error {
	if usage.ReportedMicros < 0 || usage.EstimatedMicros < 0 || usage.ReservedMicros != 0 || usage.UnsettledAttempts != 0 {
		return core.E("invalid_usage", "settlement requires nonnegative costs and no remaining reservation")
	}
	if usage.CostKnown {
		if usage.EstimatedMicros != 0 {
			return core.E("invalid_usage", "reported settlement cannot also include an estimate")
		}
	} else if usage.EstimatedMicros <= 0 || usage.ReportedMicros != 0 {
		return core.E("invalid_usage", "settlement requires a known reported charge or the declared worst-case estimate")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var j string
	if err = tx.QueryRowContext(ctx, `SELECT data FROM attempts WHERE id=?`, id).Scan(&j); err != nil {
		return notFound(err)
	}
	var a core.Attempt
	if err = json.Unmarshal([]byte(j), &a); err != nil {
		return err
	}
	if a.Status == "in_flight" {
		return core.E("conflict", "an active upstream attempt cannot be reconciled yet")
	}
	if !usage.CostKnown && usage.EstimatedMicros != a.MaxMicros {
		return core.E("invalid_usage", "worst-case settlement must equal the attempt's original cost ceiling")
	}
	var reported, estimated int64
	var known bool
	err = tx.QueryRowContext(ctx, `SELECT reported,estimated,cost_known FROM settlements WHERE attempt_id=?`, id).Scan(&reported, &estimated, &known)
	if err == nil {
		if reported == usage.ReportedMicros && estimated == usage.EstimatedMicros && known == usage.CostKnown {
			return nil
		}
		return core.E("settlement_conflict", "attempt already has a different administrative settlement")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if a.Usage.ReservedMicros == 0 {
		if a.Usage.ReportedMicros == usage.ReportedMicros && a.Usage.EstimatedMicros == usage.EstimatedMicros && a.Usage.CostKnown == usage.CostKnown {
			return nil
		}
		return core.E("settlement_conflict", "attempt is already settled; existing accounting cannot be overwritten")
	}
	a.Usage.ReportedMicros = usage.ReportedMicros
	a.Usage.EstimatedMicros = usage.EstimatedMicros
	a.Usage.ReservedMicros = 0
	a.Usage.UnsettledAttempts = 0
	a.Usage.CostKnown = usage.CostKnown
	j, err = marshal(a)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE attempts SET reported=?,estimated=?,reserved=0,data=? WHERE id=?`, usage.ReportedMicros, usage.EstimatedMicros, j, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO settlements(attempt_id,run_id,reported,estimated,cost_known,created_at) VALUES(?,?,?,?,?,?)`, id, a.RunID, usage.ReportedMicros, usage.EstimatedMicros, usage.CostKnown, now()); err != nil {
		return err
	}
	var status string
	var resultJSON sql.NullString
	var expired bool
	if err = tx.QueryRowContext(ctx, `SELECT status,result,expired FROM runs WHERE id=?`, a.RunID).Scan(&status, &resultJSON, &expired); err != nil {
		return err
	}
	if core.Terminal(status) {
		attempts, err := attemptsQuery(ctx, tx, a.RunID)
		if err != nil {
			return err
		}
		total := sumUsage(attempts)
		if _, err = tx.ExecContext(ctx, `UPDATE ledger SET reported=?,estimated=?,reserved=?,unsettled=?,updated_at=? WHERE run_id=?`, total.ReportedMicros, total.EstimatedMicros, total.ReservedMicros, total.UnsettledAttempts, now(), a.RunID); err != nil {
			return err
		}
		if !expired && resultJSON.Valid {
			var result core.Result
			if err = json.Unmarshal([]byte(resultJSON.String), &result); err != nil {
				return err
			}
			result.Attempts = attempts
			result.Usage = total
			j, err = marshal(result)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE runs SET result=? WHERE id=?`, j, a.RunID); err != nil {
				return err
			}
		}
	}
	if err = event(ctx, tx, a.RunID, "settlement", map[string]any{"attempt_id": id, "reported_micros": usage.ReportedMicros, "estimated_micros": usage.EstimatedMicros, "cost_known": usage.CostKnown}); err != nil {
		return err
	}
	return tx.Commit()
}
