package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samekind/soseek/internal/core"
)

func adminFixture(t *testing.T) (*Store, core.Principal, string) {
	t.Helper()
	s := openTest(t)
	p, token, err := s.CreatePrincipal(context.Background(), "administrator", 1000000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.GrantAdmin(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	return s, p, token
}

func TestAdminSessionsRequireCurrentRoleAndExpire(t *testing.T) {
	ctx := context.Background()
	s, admin, token := adminFixture(t)
	caller, callerToken, err := s.CreatePrincipal(ctx, "caller", 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if yes, err := s.IsAdmin(ctx, caller.ID); err != nil || yes {
		t.Fatalf("ordinary caller is admin: %v %v", yes, err)
	}
	_, _, err = s.CreateSession(ctx, callerToken)
	requireCode(t, err, "forbidden")
	_, _, err = s.CreateSession(ctx, "invalid")
	requireCode(t, err, "unauthorized")
	raw, p, err := s.CreateSession(ctx, token)
	if err != nil || p.ID != admin.ID {
		t.Fatalf("create session: %v %v", p, err)
	}
	var digest string
	var created, expires int64
	if err = s.db.QueryRow(`SELECT token_hash,created_at,expires_at FROM admin_sessions WHERE principal_id=?`, admin.ID).Scan(&digest, &created, &expires); err != nil {
		t.Fatal(err)
	}
	if digest == raw || len(digest) != 64 || time.Duration(expires-created) != 12*time.Hour {
		t.Fatal("session entropy, hashing or TTL incorrect")
	}
	if _, err = s.AuthenticateSession(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`DELETE FROM admin_roles WHERE principal_id=?`, admin.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.AuthenticateSession(ctx, raw)
	requireCode(t, err, "unauthorized")
	if err = s.GrantAdmin(ctx, admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE admin_sessions SET expires_at=? WHERE token_hash=?`, time.Now().Add(-time.Second).UnixNano(), hash(raw)); err != nil {
		t.Fatal(err)
	}
	_, err = s.AuthenticateSession(ctx, raw)
	requireCode(t, err, "unauthorized")
	newRaw, _, err := s.CreateSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteSession(ctx, newRaw); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteSession(ctx, newRaw); err != nil {
		t.Fatal(err)
	}
	_, err = s.AuthenticateSession(ctx, newRaw)
	requireCode(t, err, "unauthorized")
}

func TestAdminSessionsPersistAndLegacyRevokeInvalidates(t *testing.T) {
	ctx := context.Background()
	s, p, token := adminFixture(t)
	raw, _, err := s.CreateSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	path := s.path
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err = reopened.AuthenticateSession(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if err = reopened.Revoke(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	_, err = reopened.AuthenticateSession(ctx, raw)
	requireCode(t, err, "unauthorized")
	if yes, err := reopened.IsAdmin(ctx, p.ID); err != nil || yes {
		t.Fatalf("revoked principal remains authorized admin: %v %v", yes, err)
	}
	var version int
	if err = reopened.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("schema compatibility changed: %d %v", version, err)
	}
}

func TestAdminTokenRotationPreservesIdentityAndHistory(t *testing.T) {
	ctx := context.Background()
	s, admin, _ := adminFixture(t)
	p, oldToken, err := s.AdminCreatePrincipal(ctx, admin.ID, "caller", 500, []string{"tavily", "exa", "exa"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Admin || p.Revoked || len(p.AllowedProviders) != 2 {
		t.Fatalf("new caller privileges incorrect: %+v", p)
	}
	r := submit(t, s, p.Principal, "keep-history", 100)
	updated, err := s.AdminUpdatePrincipal(ctx, admin.ID, p.ID, "renamed", 600, []string{"exa"})
	if err != nil {
		t.Fatal(err)
	}
	newToken, err := s.AdminRotateToken(ctx, admin.ID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if newToken == oldToken {
		t.Fatal("token did not rotate")
	}
	_, err = s.Authenticate(ctx, oldToken)
	requireCode(t, err, "unauthorized")
	authed, err := s.Authenticate(ctx, newToken)
	if err != nil || authed.ID != p.ID || authed.Name != updated.Name || authed.DailyMicros != 600 || !authed.Allows("exa") || authed.Allows("tavily") {
		t.Fatalf("rotation changed identity/permissions: %+v %v", authed, err)
	}
	if _, err = s.GetRun(ctx, p.ID, r.ID); err != nil {
		t.Fatal("rotation lost existing task", err)
	}
	u, err := s.Usage(ctx, p.ID)
	if err != nil || u["reserved_micros"].(int64) != 100 {
		t.Fatalf("rotation lost budget history: %v %v", u, err)
	}
	principals, err := s.ListAdminPrincipals(ctx, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(principals)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{oldToken, newToken, hash(newToken), "token_hash"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("principal listing exposes token material")
		}
	}
}

func TestAdminRotationInvalidatesBrowserSessions(t *testing.T) {
	ctx := context.Background()
	s, p, oldToken := adminFixture(t)
	raw, _, err := s.CreateSession(ctx, oldToken)
	if err != nil {
		t.Fatal(err)
	}
	newToken, err := s.AdminRotateToken(ctx, p.ID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.AuthenticateSession(ctx, raw)
	requireCode(t, err, "unauthorized")
	_, err = s.Authenticate(ctx, oldToken)
	requireCode(t, err, "unauthorized")
	if _, _, err = s.CreateSession(ctx, newToken); err != nil {
		t.Fatal("new token cannot reestablish session", err)
	}
}

func TestAdminRevocationProtectsAdministratorAndCancelsCaller(t *testing.T) {
	ctx := context.Background()
	s, admin, token := adminFixture(t)
	err := s.AdminRevokePrincipal(ctx, admin.ID, admin.ID)
	requireCode(t, err, "conflict")
	if _, err = s.Authenticate(ctx, token); err != nil {
		t.Fatal("self revoke locked out administrator", err)
	}
	p, raw, err := s.AdminCreatePrincipal(ctx, admin.ID, "caller", 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := submit(t, s, p.Principal, "queued", 100)
	if err = s.AdminRevokePrincipal(ctx, admin.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.AdminRevokePrincipal(ctx, admin.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.Authenticate(ctx, raw)
	requireCode(t, err, "unauthorized")
	got, err := s.GetRun(ctx, p.ID, r.ID)
	if err != nil || got.Status != "cancelled" || got.Result == nil {
		t.Fatalf("revoke did not finalize queued task: %v %v", got, err)
	}
	principals, err := s.ListAdminPrincipals(ctx, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range principals {
		if v.ID == p.ID {
			found = v.Revoked
		}
	}
	if !found {
		t.Fatal("revoked identity omitted from admin listing")
	}
	_, err = s.AdminRotateToken(ctx, admin.ID, p.ID)
	requireCode(t, err, "conflict")
}

func TestConcurrentAdminRevocationCannotLockOutLastAdmin(t *testing.T) {
	ctx := context.Background()
	s, a, _ := adminFixture(t)
	b, _, err := s.CreatePrincipal(ctx, "second-admin", 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.GrantAdmin(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	var success atomic.Int64
	var wg sync.WaitGroup
	for _, pair := range [][2]string{{a.ID, b.ID}, {b.ID, a.ID}} {
		wg.Add(1)
		go func(pair [2]string) {
			defer wg.Done()
			if err := s.AdminRevokePrincipal(ctx, pair[0], pair[1]); err == nil {
				success.Add(1)
			}
		}(pair)
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatalf("expected exactly one successful revocation, got %d", success.Load())
	}
	var n int
	if err = s.db.QueryRow(`SELECT count(*) FROM admin_roles a JOIN principals p ON p.id=a.principal_id WHERE p.revoked=0`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("last administrator lost: %d %v", n, err)
	}
}

func TestAdminMutationsAndAuditAreAtomicAndSecretFree(t *testing.T) {
	ctx := context.Background()
	s, admin, adminToken := adminFixture(t)
	p, raw, err := s.AdminCreatePrincipal(ctx, admin.ID, "caller", 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := s.CreateSession(ctx, adminToken)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := s.AdminRotateToken(ctx, admin.ID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListAudit(ctx, admin.ID, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{adminToken, raw, rotated, session, hash(raw), hash(session)} {
		if strings.Contains(string(b), secret) {
			t.Fatal("audit exposes secret or token hash")
		}
	}
	if err = s.RecordAudit(ctx, admin.ID, "credential.changed", raw); err == nil {
		t.Fatal("audit accepted a raw token")
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_admin_audit BEFORE INSERT ON admin_audit BEGIN SELECT RAISE(ABORT,'audit disabled for atomicity test'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AdminCreatePrincipal(ctx, admin.ID, "must-not-exist", 1000, nil); err == nil {
		t.Fatal("mutation succeeded without audit")
	}
	var n int
	if err = s.db.QueryRow(`SELECT count(*) FROM principals WHERE name='must-not-exist'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("failed audit left created principal: %d %v", n, err)
	}
	if _, err = s.AdminRotateToken(ctx, admin.ID, p.ID); err == nil {
		t.Fatal("rotation succeeded without audit")
	}
	if _, err = s.Authenticate(ctx, rotated); err != nil {
		t.Fatal("failed audit still invalidated old token", err)
	}
	if err = s.AdminRevokePrincipal(ctx, admin.ID, p.ID); err == nil {
		t.Fatal("revocation succeeded without audit")
	}
	if _, err = s.Authenticate(ctx, rotated); err != nil {
		t.Fatal("failed audit still revoked caller", err)
	}
}

func TestAdminReadsAndMutationsDenyOrdinaryCaller(t *testing.T) {
	ctx := context.Background()
	s, admin, _ := adminFixture(t)
	caller := person(t, s, "caller", 1000)
	_, err := s.ListAdminPrincipals(ctx, caller.ID)
	requireCode(t, err, "forbidden")
	_, err = s.ListAdminRuns(ctx, caller.ID, 10, "")
	requireCode(t, err, "forbidden")
	_, err = s.ListAudit(ctx, caller.ID, 10, "")
	requireCode(t, err, "forbidden")
	_, err = s.AdminOverview(ctx, caller.ID)
	requireCode(t, err, "forbidden")
	_, _, err = s.AdminCreatePrincipal(ctx, caller.ID, "no", 100, nil)
	requireCode(t, err, "forbidden")
	_, err = s.AdminUpdatePrincipal(ctx, caller.ID, admin.ID, "changed", 100, nil)
	requireCode(t, err, "forbidden")
	_, err = s.AdminRotateToken(ctx, caller.ID, admin.ID)
	requireCode(t, err, "forbidden")
	err = s.AdminRevokePrincipal(ctx, caller.ID, admin.ID)
	requireCode(t, err, "forbidden")
	err = s.RecordAudit(ctx, caller.ID, "credential.changed", "exa")
	requireCode(t, err, "forbidden")
}

func TestAdminRunAndAuditPagination(t *testing.T) {
	ctx := context.Background()
	s, admin, _ := adminFixture(t)
	caller := person(t, s, "caller", 10000)
	expected := map[string]bool{}
	for i := 0; i < 5; i++ {
		r := submit(t, s, caller, fmt.Sprintf("page-%d", i), 100)
		expected[r.ID] = true
	}
	// Exercise the ID tie-breaker instead of relying on distinct nanoseconds.
	if _, err := s.db.Exec(`UPDATE runs SET created_at=?`, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := s.ListAdminRuns(ctx, admin.ID, 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 2 {
			t.Fatal("page limit ignored")
		}
		for _, v := range page.Items {
			if seen[v.ID] || !expected[v.ID] {
				t.Fatal("pagination duplicated or invented task")
			}
			seen[v.ID] = true
			if v.PrincipalID != caller.ID || v.PrincipalName != "caller" || v.Usage.ReservedMicros != 100 {
				t.Fatalf("summary incomplete: %+v", v)
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 5 {
		t.Fatal("pagination omitted tasks")
	}
	for i := 0; i < 5; i++ {
		if err := s.RecordAudit(ctx, admin.ID, "credential.changed", fmt.Sprintf("channel-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	seenAudit := map[int64]bool{}
	cursor = ""
	for {
		page, err := s.ListAudit(ctx, admin.ID, 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range page.Items {
			if seenAudit[v.ID] {
				t.Fatal("duplicate audit row")
			}
			seenAudit[v.ID] = true
			if v.ActorName == "" {
				t.Fatal("missing audit actor name")
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seenAudit) != 6 {
		t.Fatalf("lost audit rows: %d", len(seenAudit))
	}
	_, err := s.ListAdminRuns(ctx, admin.ID, 101, "")
	requireCode(t, err, "invalid_request")
	_, err = s.ListAdminRuns(ctx, admin.ID, 10, "invalid")
	requireCode(t, err, "invalid_request")
	_, err = s.ListAudit(ctx, admin.ID, 10, "-1")
	requireCode(t, err, "invalid_request")
}

func TestAdminOverviewUTCAccountingAndSummaryPrivacy(t *testing.T) {
	ctx := context.Background()
	s, admin, _ := adminFixture(t)
	caller := person(t, s, "caller", 10000)
	r := submit(t, s, caller, "today", 100)
	claim(t, s)
	a, err := s.BeginAttempt(ctx, r.ID, core.Attempt{MaxMicros: 50})
	if err != nil {
		t.Fatal(err)
	}
	a.Status = "succeeded"
	a.Usage = core.Usage{ReportedMicros: 20}
	if err = s.EndAttempt(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(ctx, r.ID, core.Result{Status: "succeeded", Results: []core.Source{{Content: "must-never-appear-in-summary"}}}); err != nil {
		t.Fatal(err)
	}
	old := submit(t, s, caller, "old", 100)
	if _, err = s.db.Exec(`UPDATE ledger SET accepted_day='2000-01-01' WHERE run_id=?`, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE runs SET created_at=? WHERE id=?`, time.Now().Add(-48*time.Hour).UnixNano(), old.ID); err != nil {
		t.Fatal(err)
	}
	overview, err := s.AdminOverview(ctx, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Statistics.TodayRuns != 1 || overview.Statistics.SucceededRuns != 1 || overview.Statistics.QueuedRuns != 1 || overview.Usage.ReportedMicros != 20 || overview.Usage.ReservedMicros != 100 {
		t.Fatalf("overview omitted historical risk or UTC day: %+v", overview)
	}
	page, err := s.ListAdminRuns(ctx, admin.ID, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "must-never-appear-in-summary") {
		t.Fatal("summary loaded result body")
	}
	owner, err := s.RunOwner(ctx, r.ID)
	if err != nil || owner != caller.ID {
		t.Fatalf("wrong task owner: %s %v", owner, err)
	}
	longReq, _ := json.Marshal(core.Request{Operation: "search", Query: strings.Repeat("中", 300)})
	if _, err = s.db.Exec(`UPDATE runs SET request=? WHERE id=?`, string(longReq), r.ID); err != nil {
		t.Fatal(err)
	}
	page, err = s.ListAdminRuns(ctx, admin.ID, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range page.Items {
		if v.ID == r.ID && (!v.QueryTruncated || len([]rune(v.Query)) != 240) {
			t.Fatal("long query was not safely summarized")
		}
	}
}
