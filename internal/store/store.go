// Package store provides the single-instance durable task and accounting store.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/samekind/soseek/internal/core"
	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	path string
}

var _ core.ExecutionStore = (*Store)(nil)

// Open uses one SQLite connection: transactions serialize reservations without
// holding locks across network calls. The database belongs in a private directory.
func Open(path string) (*Store, error) {
	if path == "" || path == ":memory:" {
		return nil, fmt.Errorf("a persistent database path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = privateDir(filepath.Dir(abs)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err = os.Chmod(abs, 0600); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	// Taking the writer reservation at transaction start also makes an admin
	// process sharing this local database wait safely instead of failing a
	// deferred read-to-write lock upgrade.
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, path: abs}
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;`); err != nil {
		db.Close()
		return nil, err
	}
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version > 1 {
		db.Close()
		return nil, fmt.Errorf("database schema version %d is newer than this binary supports", version)
	}
	tx, err := db.Begin()
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err = tx.Exec(schema); err != nil {
		tx.Rollback()
		db.Close()
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		db.Close()
		return nil, err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if e := os.Chmod(abs+suffix, 0600); e != nil && !os.IsNotExist(e) {
			db.Close()
			return nil, e
		}
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("use a dedicated private state directory with mode 0700; existing directory %s was not modified", path)
	}
	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS principals (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE,
 daily_micros INTEGER NOT NULL CHECK(daily_micros>0), providers TEXT NOT NULL,
 revoked INTEGER NOT NULL DEFAULT 0, last_claimed INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS runs (
 id TEXT PRIMARY KEY, principal_id TEXT NOT NULL REFERENCES principals(id),
 status TEXT NOT NULL, request TEXT NOT NULL, plan TEXT NOT NULL, principal TEXT NOT NULL,
 revision TEXT NOT NULL, budget INTEGER NOT NULL, created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL, deadline INTEGER NOT NULL, cancel_requested INTEGER NOT NULL DEFAULT 0,
 result TEXT, error TEXT, checkpoint TEXT, expired INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS runs_queue ON runs(status, created_at);
CREATE INDEX IF NOT EXISTS runs_owner ON runs(principal_id, created_at);
CREATE TABLE IF NOT EXISTS idempotency (
 principal_id TEXT NOT NULL, key TEXT NOT NULL, request_hash TEXT NOT NULL,
 run_id TEXT NOT NULL REFERENCES runs(id), PRIMARY KEY(principal_id,key)
);
CREATE TABLE IF NOT EXISTS ledger (
 run_id TEXT PRIMARY KEY, principal_id TEXT NOT NULL, accepted_day TEXT NOT NULL,
 reported INTEGER NOT NULL DEFAULT 0, estimated INTEGER NOT NULL DEFAULT 0,
 reserved INTEGER NOT NULL DEFAULT 0, unsettled INTEGER NOT NULL DEFAULT 0,
 updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ledger_day ON ledger(accepted_day, principal_id);
CREATE TABLE IF NOT EXISTS attempts (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), status TEXT NOT NULL,
 reported INTEGER NOT NULL DEFAULT 0, estimated INTEGER NOT NULL DEFAULT 0,
 reserved INTEGER NOT NULL DEFAULT 0, data TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS attempts_run ON attempts(run_id);
CREATE TABLE IF NOT EXISTS settlements (
 attempt_id TEXT PRIMARY KEY REFERENCES attempts(id), run_id TEXT NOT NULL,
 reported INTEGER NOT NULL, estimated INTEGER NOT NULL, cost_known INTEGER NOT NULL,
 created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
 id INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT NOT NULL REFERENCES runs(id),
 kind TEXT NOT NULL, data TEXT NOT NULL, created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS events_run ON events(run_id,id);
CREATE TABLE IF NOT EXISTS cache (
 principal_id TEXT NOT NULL, key TEXT NOT NULL, data TEXT NOT NULL, expires_at INTEGER NOT NULL,
 PRIMARY KEY(principal_id,key)
);
PRAGMA user_version=1;
`

func randomID(prefix string) (string, error) {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}
func hash(s string) string          { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func marshal(v any) (string, error) { b, e := json.Marshal(v); return string(b), e }
func now() int64                    { return time.Now().UTC().UnixNano() }
func stamp(n int64) time.Time       { return time.Unix(0, n).UTC() }
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return core.E("not_found", "resource not found")
	}
	return err
}

type querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}
type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func event(ctx context.Context, q execer, id, kind string, data any) error {
	b, e := marshal(data)
	if e != nil {
		return e
	}
	_, e = q.ExecContext(ctx, `INSERT INTO events(run_id,kind,data,created_at) VALUES(?,?,?,?)`, id, kind, b, now())
	return e
}

func (s *Store) CreatePrincipal(ctx context.Context, name string, dailyMicros int64, providers []string) (core.Principal, string, error) {
	if strings.TrimSpace(name) == "" || dailyMicros <= 0 {
		return core.Principal{}, "", core.E("invalid_request", "name and positive daily budget required")
	}
	id, e := randomID("usr_")
	if e != nil {
		return core.Principal{}, "", e
	}
	b := make([]byte, 32)
	if _, e = rand.Read(b); e != nil {
		return core.Principal{}, "", e
	}
	token := "ssk_" + base64.RawURLEncoding.EncodeToString(b)
	p := core.Principal{ID: id, Name: name, DailyMicros: dailyMicros, AllowedProviders: providers}
	pj, e := marshal(providers)
	if e != nil {
		return core.Principal{}, "", e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO principals(id,name,token_hash,daily_micros,providers) VALUES(?,?,?,?,?)`, id, name, hash(token), dailyMicros, pj)
	if e != nil {
		return core.Principal{}, "", e
	}
	return p, token, nil
}
func (s *Store) Authenticate(ctx context.Context, token string) (core.Principal, error) {
	var p core.Principal
	var providers string
	e := s.db.QueryRowContext(ctx, `SELECT id,name,daily_micros,providers FROM principals WHERE token_hash=? AND revoked=0`, hash(token)).Scan(&p.ID, &p.Name, &p.DailyMicros, &providers)
	if errors.Is(e, sql.ErrNoRows) {
		return p, core.E("unauthorized", "invalid or revoked access token")
	}
	if e != nil {
		return p, e
	}
	e = json.Unmarshal([]byte(providers), &p.AllowedProviders)
	return p, e
}
func (s *Store) ListPrincipals(ctx context.Context) ([]core.Principal, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT id,name,daily_micros,providers FROM principals WHERE revoked=0 ORDER BY name,id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []core.Principal{}
	for rows.Next() {
		var p core.Principal
		var j string
		if e = rows.Scan(&p.ID, &p.Name, &p.DailyMicros, &j); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(j), &p.AllowedProviders); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) Revoke(ctx context.Context, principalID string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	r, e := tx.ExecContext(ctx, `UPDATE principals SET revoked=1 WHERE id=?`, principalID)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return core.E("not_found", "principal not found")
	}
	rows, e := tx.QueryContext(ctx, `SELECT id,status FROM runs WHERE principal_id=? AND status IN ('queued','running')`, principalID)
	if e != nil {
		return e
	}
	type revocation struct{ id, status string }
	var tasks []revocation
	for rows.Next() {
		var task revocation
		if e = rows.Scan(&task.id, &task.status); e != nil {
			rows.Close()
			return e
		}
		tasks = append(tasks, task)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, task := range tasks {
		if _, e = tx.ExecContext(ctx, `UPDATE runs SET cancel_requested=1,updated_at=? WHERE id=?`, now(), task.id); e != nil {
			return e
		}
		if task.status == "queued" {
			e = finishTx(ctx, tx, task.id, core.Result{Status: "cancelled", StopReason: "credential_revoked"})
		} else {
			e = event(ctx, tx, task.id, "cancel_requested", map[string]string{"reason": "credential_revoked"})
		}
		if e != nil {
			return e
		}
	}
	return tx.Commit()
}
