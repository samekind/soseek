package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/samekind/soseek/internal/core"
)

const AdminSessionTTL = 12 * time.Hour

type AdminPrincipal struct {
	core.Principal
	Admin   bool `json:"admin"`
	Revoked bool `json:"revoked"`
}

type AdminUsage struct {
	ReportedMicros  int64 `json:"reported_micros"`
	EstimatedMicros int64 `json:"estimated_micros"`
	ReservedMicros  int64 `json:"reserved_micros"`
}
type AdminStatistics struct {
	TodayRuns     int64 `json:"today_runs"`
	ActiveRuns    int64 `json:"active_runs"`
	QueuedRuns    int64 `json:"queued_runs"`
	SucceededRuns int64 `json:"succeeded_runs"`
	FailedRuns    int64 `json:"failed_runs"`
	PartialRuns   int64 `json:"partial_runs"`
}
type Overview struct {
	Statistics AdminStatistics `json:"statistics"`
	Usage      AdminUsage      `json:"usage"`
}
type AdminRunSummary struct {
	ID             string     `json:"run_id"`
	PrincipalID    string     `json:"principal_id"`
	PrincipalName  string     `json:"principal_name"`
	Operation      string     `json:"operation"`
	Query          string     `json:"query,omitempty"`
	URL            string     `json:"url,omitempty"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	Usage          AdminUsage `json:"usage"`
	Expired        bool       `json:"expired"`
	QueryTruncated bool       `json:"query_truncated,omitempty"`
}
type AdminRunPage struct {
	Items      []AdminRunSummary `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}
type AuditEntry struct {
	ID        int64     `json:"id"`
	ActorID   string    `json:"actor_id"`
	ActorName string    `json:"actor_name"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	CreatedAt time.Time `json:"created_at"`
}
type AuditPage struct {
	Items      []AuditEntry `json:"items"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

func isAdmin(ctx context.Context, q querier, id string) (bool, error) {
	var yes bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admin_roles a JOIN principals p ON p.id=a.principal_id WHERE p.id=? AND p.revoked=0)`, id).Scan(&yes)
	return yes, err
}
func (s *Store) IsAdmin(ctx context.Context, id string) (bool, error) { return isAdmin(ctx, s.db, id) }
func requireAdmin(ctx context.Context, q querier, id string) error {
	yes, err := isAdmin(ctx, q, id)
	if err != nil {
		return err
	}
	if !yes {
		return core.E("forbidden", "an active administrator is required")
	}
	return nil
}
func (s *Store) adminTx(ctx context.Context, actorID string) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err = requireAdmin(ctx, tx, actorID); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}
func adminAudit(ctx context.Context, q execer, actorID, action, target string) error {
	_, err := q.ExecContext(ctx, `INSERT INTO admin_audit(actor_id,action,target,created_at) VALUES(?,?,?,?)`, actorID, action, target, now())
	return err
}

// GrantAdmin is a local bootstrap operation, deliberately absent from browser
// principal management. Its audit identifies the trusted local CLI as the actor.
func (s *Store) GrantAdmin(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var revoked bool
	if err = tx.QueryRowContext(ctx, `SELECT revoked FROM principals WHERE id=?`, id).Scan(&revoked); err != nil {
		return notFound(err)
	}
	if revoked {
		return core.E("conflict", "a revoked principal cannot be granted administration")
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO admin_roles(principal_id,granted_at) VALUES(?,?) ON CONFLICT(principal_id) DO NOTHING`, id, now())
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n > 0 {
		if err = adminAudit(ctx, tx, "local-cli", "admin.granted", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func opaqueToken(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}
func readPrincipal(row scanner) (core.Principal, error) {
	var p core.Principal
	var providers string
	if err := row.Scan(&p.ID, &p.Name, &p.DailyMicros, &providers); err != nil {
		return p, err
	}
	err := json.Unmarshal([]byte(providers), &p.AllowedProviders)
	return p, err
}

// CreateSession exchanges an existing administrator API token for an opaque,
// non-sliding 12-hour browser session. Only SHA-256 hashes are persisted.
func (s *Store) CreateSession(ctx context.Context, token string) (string, core.Principal, error) {
	var empty core.Principal
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", empty, err
	}
	defer tx.Rollback()
	p, err := readPrincipal(tx.QueryRowContext(ctx, `SELECT id,name,daily_micros,providers FROM principals WHERE token_hash=? AND revoked=0`, hash(token)))
	if errors.Is(err, sql.ErrNoRows) {
		return "", empty, core.E("unauthorized", "invalid or revoked access token")
	}
	if err != nil {
		return "", empty, err
	}
	if err = requireAdmin(ctx, tx, p.ID); err != nil {
		return "", empty, err
	}
	raw, err := opaqueToken("sss_")
	if err != nil {
		return "", empty, err
	}
	t := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, `DELETE FROM admin_sessions WHERE expires_at<=?`, t.UnixNano()); err != nil {
		return "", empty, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO admin_sessions(token_hash,principal_id,created_at,expires_at) VALUES(?,?,?,?)`, hash(raw), p.ID, t.UnixNano(), t.Add(AdminSessionTTL).UnixNano()); err != nil {
		return "", empty, err
	}
	if err = adminAudit(ctx, tx, p.ID, "session.created", p.ID); err != nil {
		return "", empty, err
	}
	if err = tx.Commit(); err != nil {
		return "", empty, err
	}
	return raw, p, nil
}

// AuthenticateSession checks current principal state and admin role on every
// request; revoking a principal or role immediately invalidates all sessions.
func (s *Store) AuthenticateSession(ctx context.Context, raw string) (core.Principal, error) {
	p, err := readPrincipal(s.db.QueryRowContext(ctx, `SELECT p.id,p.name,p.daily_micros,p.providers FROM admin_sessions s JOIN principals p ON p.id=s.principal_id JOIN admin_roles a ON a.principal_id=p.id WHERE s.token_hash=? AND s.expires_at>? AND p.revoked=0`, hash(raw), now()))
	if errors.Is(err, sql.ErrNoRows) {
		return p, core.E("unauthorized", "invalid or expired administrator session")
	}
	return p, err
}
func (s *Store) DeleteSession(ctx context.Context, raw string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var principalID string
	err = tx.QueryRowContext(ctx, `SELECT principal_id FROM admin_sessions WHERE token_hash=?`, hash(raw)).Scan(&principalID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM admin_sessions WHERE token_hash=?`, hash(raw)); err != nil {
		return err
	}
	if err = adminAudit(ctx, tx, principalID, "session.deleted", principalID); err != nil {
		return err
	}
	return tx.Commit()
}

const adminPrincipalColumns = `p.id,p.name,p.daily_micros,p.providers,p.revoked,EXISTS(SELECT 1 FROM admin_roles a WHERE a.principal_id=p.id)`

func scanAdminPrincipal(row scanner) (AdminPrincipal, error) {
	var p AdminPrincipal
	var providers string
	if err := row.Scan(&p.ID, &p.Name, &p.DailyMicros, &providers, &p.Revoked, &p.Admin); err != nil {
		return p, notFound(err)
	}
	err := json.Unmarshal([]byte(providers), &p.AllowedProviders)
	return p, err
}
func adminPrincipal(ctx context.Context, q querier, id string) (AdminPrincipal, error) {
	return scanAdminPrincipal(q.QueryRowContext(ctx, `SELECT `+adminPrincipalColumns+` FROM principals p WHERE p.id=?`, id))
}
func (s *Store) ListAdminPrincipals(ctx context.Context, actorID string) ([]AdminPrincipal, error) {
	if err := requireAdmin(ctx, s.db, actorID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+adminPrincipalColumns+` FROM principals p ORDER BY p.name,p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminPrincipal{}
	for rows.Next() {
		p, err := scanAdminPrincipal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func validatePrincipal(name string, daily int64, providers []string) (string, []string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 100 || daily <= 0 || daily > 1_000_000_000_000 {
		return "", nil, core.E("invalid_request", "name must contain 1 to 100 characters and daily budget must be positive and bounded")
	}
	allowed := map[string]bool{"exa": true, "tavily": true, "firecrawl": true, "serper": true, "grok": true}
	seen := map[string]bool{}
	clean := []string{}
	for _, p := range providers {
		if !allowed[p] {
			return "", nil, core.E("invalid_request", "unknown allowed provider")
		}
		if !seen[p] {
			clean = append(clean, p)
			seen[p] = true
		}
	}
	sort.Strings(clean)
	return name, clean, nil
}

// AdminCreatePrincipal always creates a normal caller; its response is the sole
// opportunity to read the new plaintext token, which never enters the audit.
func (s *Store) AdminCreatePrincipal(ctx context.Context, actorID, name string, daily int64, providers []string) (AdminPrincipal, string, error) {
	var p AdminPrincipal
	name, providers, err := validatePrincipal(name, daily, providers)
	if err != nil {
		return p, "", err
	}
	tx, err := s.adminTx(ctx, actorID)
	if err != nil {
		return p, "", err
	}
	defer tx.Rollback()
	id, err := randomID("usr_")
	if err != nil {
		return p, "", err
	}
	raw, err := opaqueToken("ssk_")
	if err != nil {
		return p, "", err
	}
	j, err := marshal(providers)
	if err != nil {
		return p, "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO principals(id,name,token_hash,daily_micros,providers) VALUES(?,?,?,?,?)`, id, name, hash(raw), daily, j); err != nil {
		return p, "", err
	}
	if err = adminAudit(ctx, tx, actorID, "principal.created", id); err != nil {
		return p, "", err
	}
	if err = tx.Commit(); err != nil {
		return p, "", err
	}
	p.Principal = core.Principal{ID: id, Name: name, DailyMicros: daily, AllowedProviders: providers}
	return p, raw, nil
}

func (s *Store) AdminUpdatePrincipal(ctx context.Context, actorID, id, name string, daily int64, providers []string) (AdminPrincipal, error) {
	var p AdminPrincipal
	name, providers, err := validatePrincipal(name, daily, providers)
	if err != nil {
		return p, err
	}
	tx, err := s.adminTx(ctx, actorID)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	if _, err = adminPrincipal(ctx, tx, id); err != nil {
		return p, err
	}
	j, err := marshal(providers)
	if err != nil {
		return p, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE principals SET name=?,daily_micros=?,providers=? WHERE id=?`, name, daily, j, id); err != nil {
		return p, err
	}
	if err = adminAudit(ctx, tx, actorID, "principal.updated", id); err != nil {
		return p, err
	}
	p, err = adminPrincipal(ctx, tx, id)
	if err != nil {
		return p, err
	}
	if err = tx.Commit(); err != nil {
		return AdminPrincipal{}, err
	}
	return p, nil
}

func (s *Store) AdminRotateToken(ctx context.Context, actorID, id string) (string, error) {
	tx, err := s.adminTx(ctx, actorID)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	p, err := adminPrincipal(ctx, tx, id)
	if err != nil {
		return "", err
	}
	if p.Revoked {
		return "", core.E("conflict", "revoked principal cannot rotate its token")
	}
	raw, err := opaqueToken("ssk_")
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE principals SET token_hash=? WHERE id=?`, hash(raw), id); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM admin_sessions WHERE principal_id=?`, id); err != nil {
		return "", err
	}
	if err = adminAudit(ctx, tx, actorID, "principal.token_rotated", id); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return raw, nil
}

func (s *Store) AdminRevokePrincipal(ctx context.Context, actorID, id string) error {
	tx, err := s.adminTx(ctx, actorID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if actorID == id {
		return core.E("conflict", "administrators cannot revoke their own principal")
	}
	p, err := adminPrincipal(ctx, tx, id)
	if err != nil {
		return err
	}
	if p.Revoked {
		return nil
	}
	if p.Admin {
		var n int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM admin_roles a JOIN principals p ON p.id=a.principal_id WHERE p.revoked=0`).Scan(&n); err != nil {
			return err
		}
		if n <= 1 {
			return core.E("conflict", "the last active administrator cannot be revoked")
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE principals SET revoked=1 WHERE id=?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM admin_sessions WHERE principal_id=?`, id); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,status FROM runs WHERE principal_id=? AND status IN ('queued','running')`, id)
	if err != nil {
		return err
	}
	type task struct{ id, status string }
	tasks := []task{}
	for rows.Next() {
		var v task
		if err = rows.Scan(&v.id, &v.status); err != nil {
			rows.Close()
			return err
		}
		tasks = append(tasks, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range tasks {
		if _, err = tx.ExecContext(ctx, `UPDATE runs SET cancel_requested=1,updated_at=? WHERE id=?`, now(), v.id); err != nil {
			return err
		}
		if v.status == "queued" {
			err = finishTx(ctx, tx, v.id, core.Result{Status: "cancelled", StopReason: "credential_revoked"})
		} else {
			err = event(ctx, tx, v.id, "cancel_requested", map[string]string{"reason": "credential_revoked"})
		}
		if err != nil {
			return err
		}
	}
	if err = adminAudit(ctx, tx, actorID, "principal.revoked", id); err != nil {
		return err
	}
	return tx.Commit()
}

var auditAtom = regexp.MustCompile(`^[A-Za-z0-9_.:@/-]{1,128}$`)

// RecordAudit accepts resource identifiers and fixed action labels, never
// request bodies, credential values or arbitrary detail maps. Related persistent
// principal mutations use the same transaction as their audit internally.
func (s *Store) RecordAudit(ctx context.Context, actorID, action, target string) error {
	if !auditAtom.MatchString(action) || len(action) > 64 || !auditAtom.MatchString(target) || strings.Contains(target, "://") {
		return core.E("invalid_request", "audit requires a fixed action and resource identifier")
	}
	for _, prefix := range []string{"ssk_", "sss_", "tvly-", "sk-", "fc-"} {
		if strings.HasPrefix(target, prefix) {
			return core.E("invalid_request", "credentials must never be recorded in audit")
		}
	}
	tx, err := s.adminTx(ctx, actorID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = adminAudit(ctx, tx, actorID, action, target); err != nil {
		return err
	}
	return tx.Commit()
}

func pageLimit(limit int) (int, error) {
	if limit == 0 {
		return 50, nil
	}
	if limit < 1 || limit > 100 {
		return 0, core.E("invalid_request", "page limit must be between 1 and 100")
	}
	return limit, nil
}

func (s *Store) ListAudit(ctx context.Context, actorID string, limit int, cursor string) (AuditPage, error) {
	page := AuditPage{Items: []AuditEntry{}}
	if err := requireAdmin(ctx, s.db, actorID); err != nil {
		return page, err
	}
	limit, err := pageLimit(limit)
	if err != nil {
		return page, err
	}
	var before int64
	if cursor != "" {
		before, err = strconv.ParseInt(cursor, 10, 64)
		if err != nil || before <= 0 {
			return page, core.E("invalid_request", "invalid audit cursor")
		}
	}
	query := `SELECT a.id,a.actor_id,COALESCE(p.name,a.actor_id),a.action,a.target,a.created_at FROM admin_audit a LEFT JOIN principals p ON p.id=a.actor_id`
	args := []any{}
	if before > 0 {
		query += ` WHERE a.id<?`
		args = append(args, before)
	}
	query += ` ORDER BY a.id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var v AuditEntry
		var created int64
		if err = rows.Scan(&v.ID, &v.ActorID, &v.ActorName, &v.Action, &v.Target, &created); err != nil {
			return page, err
		}
		v.CreatedAt = stamp(created)
		page.Items = append(page.Items, v)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = strconv.FormatInt(page.Items[limit-1].ID, 10)
	}
	return page, nil
}

type runCursor struct {
	Created int64  `json:"t"`
	ID      string `json:"id"`
}

func (s *Store) ListAdminRuns(ctx context.Context, actorID string, limit int, cursor string) (AdminRunPage, error) {
	page := AdminRunPage{Items: []AdminRunSummary{}}
	if err := requireAdmin(ctx, s.db, actorID); err != nil {
		return page, err
	}
	limit, err := pageLimit(limit)
	if err != nil {
		return page, err
	}
	var c runCursor
	if cursor != "" {
		if len(cursor) > 512 {
			return page, core.E("invalid_request", "invalid task cursor")
		}
		b, e := base64.RawURLEncoding.DecodeString(cursor)
		if e != nil || json.Unmarshal(b, &c) != nil || c.Created <= 0 || c.ID == "" || len(c.ID) > 128 {
			return page, core.E("invalid_request", "invalid task cursor")
		}
	}
	query := `SELECT r.id,r.principal_id,p.name,COALESCE(json_extract(r.request,'$.operation'),''),COALESCE(json_extract(r.request,'$.query'),''),COALESCE(json_extract(r.request,'$.url'),''),r.status,r.created_at,r.updated_at,r.expired,COALESCE(l.reported,0),COALESCE(l.estimated,0),COALESCE(l.reserved,0) FROM runs r JOIN principals p ON p.id=r.principal_id LEFT JOIN ledger l ON l.run_id=r.id`
	args := []any{}
	if cursor != "" {
		query += ` WHERE (r.created_at,r.id)<(?,?)`
		args = append(args, c.Created, c.ID)
	}
	query += ` ORDER BY r.created_at DESC,r.id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var v AdminRunSummary
		var created, updated int64
		if err = rows.Scan(&v.ID, &v.PrincipalID, &v.PrincipalName, &v.Operation, &v.Query, &v.URL, &v.Status, &created, &updated, &v.Expired, &v.Usage.ReportedMicros, &v.Usage.EstimatedMicros, &v.Usage.ReservedMicros); err != nil {
			return page, err
		}
		v.CreatedAt = stamp(created)
		v.UpdatedAt = stamp(updated)
		q := []rune(v.Query)
		if len(q) > 240 {
			v.Query = string(q[:240])
			v.QueryTruncated = true
		}
		page.Items = append(page.Items, v)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		b, _ := json.Marshal(runCursor{last.CreatedAt.UnixNano(), last.ID})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return page, nil
}

// RunOwner supplies the existing owner-scoped GetRun path after API admin
// authorization. The regular caller API must never expose this helper directly.
func (s *Store) RunOwner(ctx context.Context, id string) (string, error) {
	var owner string
	err := s.db.QueryRowContext(ctx, `SELECT principal_id FROM runs WHERE id=?`, id).Scan(&owner)
	return owner, notFound(err)
}

func (s *Store) AdminOverview(ctx context.Context, actorID string) (Overview, error) {
	var out Overview
	if err := requireAdmin(ctx, s.db, actorID); err != nil {
		return out, err
	}
	t := time.Now().UTC()
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN created_at>=? AND created_at<? THEN 1 ELSE 0 END),0),COALESCE(SUM(status='running'),0),COALESCE(SUM(status='queued'),0),COALESCE(SUM(status='succeeded'),0),COALESCE(SUM(status='failed'),0),COALESCE(SUM(status='partial'),0) FROM runs`, start.UnixNano(), end.UnixNano()).Scan(&out.Statistics.TodayRuns, &out.Statistics.ActiveRuns, &out.Statistics.QueuedRuns, &out.Statistics.SucceededRuns, &out.Statistics.FailedRuns, &out.Statistics.PartialRuns)
	if err != nil {
		return out, err
	}
	day := t.Format("2006-01-02")
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN accepted_day=? THEN reported ELSE 0 END),0),COALESCE(SUM(CASE WHEN accepted_day=? THEN estimated ELSE 0 END),0),COALESCE(SUM(reserved),0) FROM ledger`, day, day).Scan(&out.Usage.ReportedMicros, &out.Usage.EstimatedMicros, &out.Usage.ReservedMicros)
	return out, err
}
