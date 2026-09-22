package connectorjobs

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func fixture(t *testing.T) (Store, string, string, string) {
	t.Helper()
	dsn := os.Getenv("CONNECTOR_JOBS_TEST_DSN")
	if dsn == "" {
		t.Skip("CONNECTOR_JOBS_TEST_DSN requires disposable migrated app database")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Path != "/commerce_validation" {
		t.Fatal("use disposable loopback commerce_validation")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(10)
	team, user, tenant, connection, source, run := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO postgoose_users(id,email,name,"updatedAt") VALUES($1,$2,'queue fixture',now())`, user, user+"@example.invalid")
	exec(`INSERT INTO "Team"(id,name,"updatedAt") VALUES($1,'queue fixture',now())`, team)
	exec(`INSERT INTO tenants(id,name,"kmsKeyId","teamId","updatedAt") VALUES($1,'fixture','fixture',$2,now())`, tenant, team)
	exec(`INSERT INTO postgoose_user_teams(id,"userId","teamId",role,"updatedAt") VALUES($1,$2,$3,'OWNER',now())`, uuid.NewString(), user, team)
	exec(`INSERT INTO postgoose_connections(id,name,"tenantId","teamId",type,"updatedAt") VALUES($1,'fixture',$2,$3,'POSTGRESQL',now())`, connection, tenant, team)
	exec(`INSERT INTO connector_sources(id,"teamId",name,"manifestId","manifestVersion","manifestDigest",configuration,"encryptedSecrets","secretFields","destinationConnectionId","destinationDatabase","destinationSchema","updatedAt")
 VALUES($1,$2,'fixture','syne/razorpay','1.0.1',$3,'{}','{}','{}',$4,'commerce_validation','raw_commerce',now())`, source, team, strings.Repeat("a", 64), connection)
	exec(`INSERT INTO connector_sync_runs(id,"teamId","sourceId","sourceRevision","requestedById","streamId","manifestId","manifestVersion","manifestDigest","configurationSnapshot",binding,"destinationConnectionId","destinationDatabase","destinationSchema","expiresAt","updatedAt")
 VALUES($1,$2,$3,1,$4,'settlements','syne/razorpay','1.0.1',$5,'{}',$5,$6,'commerce_validation','raw_commerce',(now() AT TIME ZONE 'UTC')+interval '30 minutes',now())`, run, team, source, user, strings.Repeat("a", 64), connection)
	t.Cleanup(func() {
		for _, table := range []string{"connector_sync_runs", "connector_sources", "postgoose_connections", "postgoose_user_teams", "tenants"} {
			db.Exec(`UPDATE `+table+` SET deleted=true,"deletedAt"=now() WHERE "teamId"=$1`, team)
		}
		db.Exec(`UPDATE "Team" SET deleted=true,"deletedAt"=now() WHERE id=$1`, team)
		db.Exec(`UPDATE postgoose_users SET deleted=true,"deletedAt"=now() WHERE id=$1`, user)
		db.Close()
	})
	return Store{DB: db}, run, user, source
}

func TestConcurrentClaimAndRestartDurability(t *testing.T) {
	s, id, _, _ := fixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan *Lease, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); lease, err := s.Claim(ctx); results <- lease; errs <- err }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	var claimed Lease
	for lease := range results {
		if lease != nil {
			count++
			claimed = *lease
		}
	}
	if count != 1 || claimed.RunID != id || claimed.LeaseID == "" {
		t.Fatal("duplicate or lost claim", count)
	}
	restarted := Store{DB: s.DB}
	if lease, err := restarted.Claim(ctx); err != nil || lease != nil {
		t.Fatal("running lease was redispatched", err)
	}
	if active, err := restarted.Active(ctx, claimed); err != nil || !active {
		t.Fatal("lost persisted lease", err)
	}
	var seconds float64
	if err := s.DB.QueryRow(`SELECT EXTRACT(EPOCH FROM ("expiresAt"-"startedAt")) FROM connector_sync_runs WHERE id=$1`, id).Scan(&seconds); err != nil || seconds < 899 || seconds > 901 {
		t.Fatal("wrong lease lifetime", seconds, err)
	}
}

func TestRevocationAndCompletionRequireDurableReceipt(t *testing.T) {
	s, id, user, source := fixture(t)
	ctx := context.Background()
	if _, err := s.DB.Exec(`UPDATE connector_sources SET revision=2 WHERE id=$1`, source); err != nil {
		t.Fatal(err)
	}
	if lease, err := s.Claim(ctx); err != nil || lease != nil {
		t.Fatal("changed source admitted", err)
	}
	if _, err := s.DB.Exec(`UPDATE connector_sources SET revision=1 WHERE id=$1`, source); err != nil {
		t.Fatal(err)
	}
	lease, err := s.Claim(ctx)
	if err != nil || lease == nil {
		t.Fatal(err)
	}
	if err := s.Succeed(ctx, *lease, 1); !errors.Is(err, ErrLeaseUnavailable) {
		t.Fatal("success without receipt", err)
	}
	if _, err := s.DB.Exec(`UPDATE connector_sync_runs SET "lastReceipt"='{"sequence":3}' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Succeed(ctx, *lease, 2); !errors.Is(err, ErrLeaseUnavailable) {
		t.Fatal("wrong receipt", err)
	}
	if _, err := s.DB.Exec(`UPDATE postgoose_user_teams SET role='MEMBER' WHERE "userId"=$1`, user); err != nil {
		t.Fatal(err)
	}
	if active, err := s.Active(ctx, *lease); err != nil || active {
		t.Fatal("revoked admin active", err)
	}
	if err := s.Succeed(ctx, *lease, 3); !errors.Is(err, ErrLeaseUnavailable) {
		t.Fatal("revoked success", err)
	}
	if _, err := s.DB.Exec(`UPDATE postgoose_user_teams SET role='OWNER' WHERE "userId"=$1`, user); err != nil {
		t.Fatal(err)
	}
	if err := s.Succeed(ctx, *lease, 3); err != nil {
		t.Fatal(err)
	}
	if active, err := s.Active(ctx, *lease); err != nil || active {
		t.Fatal("completed lease still active", err)
	}
	var status string
	if err := s.DB.QueryRow(`SELECT status FROM connector_sync_runs WHERE id=$1`, id).Scan(&status); err != nil || status != "SUCCEEDED" {
		t.Fatal(status, err)
	}
}

func TestExpiryNeverReplaysAndCancellationWins(t *testing.T) {
	s, id, _, _ := fixture(t)
	ctx := context.Background()
	lease, err := s.Claim(ctx)
	if err != nil || lease == nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE connector_sync_runs SET "expiresAt"='2000-01-01' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if count, err := s.Expire(ctx); err != nil || count < 1 {
		t.Fatal(count, err)
	}
	var status, code string
	if err := s.DB.QueryRow(`SELECT status,"failureCode" FROM connector_sync_runs WHERE id=$1`, id).Scan(&status, &code); err != nil || status != "FAILED" || code != "lease_expired" {
		t.Fatal(status, code, err)
	}
	if lease, err := s.Claim(ctx); err != nil || lease != nil {
		t.Fatal("expired run replayed", err)
	}
	if _, err := s.DB.Exec(`UPDATE connector_sync_runs SET status='CANCELLED',"leaseId"=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(ctx, *lease, "worker_failed"); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT status FROM connector_sync_runs WHERE id=$1`, id).Scan(&status); err != nil || status != "CANCELLED" {
		t.Fatal("late failure overwrote cancellation", status, err)
	}
	if err := s.Fail(ctx, *lease, "secret-provider-error"); err == nil {
		t.Fatal("unreviewed failure code accepted")
	}
}
