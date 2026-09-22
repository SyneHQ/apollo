// Package connectorjobs claims persisted app requests for the isolated worker.
// Customer records and source credentials never enter Apollo's queue.
package connectorjobs

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	"github.com/google/uuid"
)

var ErrLeaseUnavailable = errors.New("connector lease unavailable")

type Scope struct {
	TeamID       string `json:"team_id"`
	SourceID     string `json:"source_id"`
	ConnectionID string `json:"connection_id"`
	Database     string `json:"database"`
	Schema       string `json:"schema"`
	Stream       string `json:"stream"`
	Binding      string `json:"binding"`
}

type Lease struct {
	RunID          string
	LeaseID        string
	UserID         string
	SourceRevision int
	Scope          Scope
	ExpiresAt      time.Time
	InstallationID string
	PolicyDigest   string
	ApprovedOrigin string
	ManifestDigest string
}

type Store struct{ DB *sql.DB }

const joins = ` FROM connector_sync_runs r
 JOIN connector_sources s ON s.id=r."sourceId" AND s."teamId"=r."teamId"
 JOIN postgoose_connections c ON c.id=r."destinationConnectionId" AND c."teamId"=r."teamId"
 JOIN tenants tenant ON tenant.id=c."tenantId"
 JOIN "Team" t ON t.id=r."teamId"
 JOIN postgoose_users u ON u.id=r."requestedById"
 JOIN postgoose_user_teams m ON m."teamId"=r."teamId" AND m."userId"=r."requestedById" `

const current = ` r.deleted=false AND s.deleted=false AND s.revision=r."sourceRevision"
 AND s."manifestDigest"=r."manifestDigest" AND s."manifestId"=r."manifestId" AND s."manifestVersion"=r."manifestVersion"
 AND s.configuration=r."configurationSnapshot"
 AND s."installationId" IS NOT DISTINCT FROM r."installationId"
 AND s."policyDigest" IS NOT DISTINCT FROM r."policyDigest"
 AND ((r."installationId" IS NULL AND r."policyDigest" IS NULL) OR
 (r."installationId" IS NOT NULL AND r."policyDigest" IS NOT NULL AND EXISTS (
 SELECT 1 FROM connector_installations i WHERE i.id=r."installationId" AND i."teamId"=r."teamId"
 AND i.deleted=false AND i."revokedAt" IS NULL AND i."manifestId"=r."manifestId"
 AND i."manifestVersion"=r."manifestVersion" AND i."manifestDigest"=r."manifestDigest"
 AND i."policyDigest"=r."policyDigest")))
 AND s."destinationConnectionId"=r."destinationConnectionId" AND s."destinationDatabase"=r."destinationDatabase"
 AND s."destinationSchema"=r."destinationSchema" AND c.deleted=false AND c.type='POSTGRESQL'
 AND t.deleted=false AND u.deleted=false AND m.deleted=false AND m.role IN ('OWNER','ADMIN')
 AND tenant.deleted=false AND tenant.status='ACTIVE'
 AND (r."fileHash" IS NULL OR EXISTS (
 SELECT 1 FROM "File" f
 JOIN postgoose_storage_destinations d ON d.id=f."storageDestinationId" AND d."teamId"=f."teamId"
 JOIN tenants ft ON ft.id=d."tenantId" AND ft."teamId"=f."teamId"
 WHERE f.id=r."configurationSnapshot"->>'file_id' AND f."teamId"=r."teamId"
 AND f.deleted=false AND d.deleted=false AND ft.deleted=false AND ft.status='ACTIVE'
 AND f.size>0 AND f.size<=52428800 AND lower(f.name) LIKE '%.csv'))
 AND r."expiresAt">(clock_timestamp() AT TIME ZONE 'UTC') `

// The private rollout flag is a live authorization gate in this process.
func liveCurrent() string {
	if os.Getenv("CONNECTOR_PRIVATE_SYNCS_ENABLED") != "true" {
		return current + ` AND r."installationId" IS NULL `
	}
	return current
}

// Lock installation before run/source rows, matching installation revocation.
// Recheck the complete live predicate after acquiring the lock.
func lockInstallation(ctx context.Context, tx *sql.Tx, runID string) error {
	var installation sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT "installationId" FROM connector_sync_runs WHERE id=$1`, runID).Scan(&installation); err != nil {
		return err
	}
	if !installation.Valid {
		return nil
	}
	if os.Getenv("CONNECTOR_PRIVATE_SYNCS_ENABLED") != "true" {
		return ErrLeaseUnavailable
	}
	var id string
	err := tx.QueryRowContext(ctx, `SELECT i.id FROM connector_installations i
 JOIN connector_sync_runs r ON r."installationId"=i.id AND r."teamId"=i."teamId"
 WHERE r.id=$1 AND i.deleted=false AND i."revokedAt" IS NULL
 AND i."manifestId"=r."manifestId" AND i."manifestVersion"=r."manifestVersion"
 AND i."manifestDigest"=r."manifestDigest" AND i."policyDigest"=r."policyDigest"
 FOR SHARE OF i`, runID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLeaseUnavailable
	}
	return err
}

func (s Store) begin(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `SET LOCAL statement_timeout='5s'`); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func audit(ctx context.Context, tx *sql.Tx, user, run, action string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO postgoose_action_logs (id,"userId",action,query,"createdAt","updatedAt",deleted)
 VALUES($1,$2,$3,$4,now() AT TIME ZONE 'UTC',now() AT TIME ZONE 'UTC',false)`, uuid.NewString(), user, "CONNECTOR_SYNC:"+action, "CONNECTOR_SYNC:"+run)
	return err
}

// Claim atomically takes one queued request. A process restart cannot lose an
// accepted request or claim the same run twice. Expired runs are never restarted.
func (s Store) Claim(ctx context.Context) (*Lease, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var lease Lease
	var candidate string
	err = tx.QueryRowContext(ctx, `SELECT r.id`+joins+` WHERE r.status='QUEUED' AND `+liveCurrent()+` ORDER BY r."createdAt",r.id LIMIT 1`).Scan(&candidate)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = lockInstallation(ctx, tx, candidate); errors.Is(err, ErrLeaseUnavailable) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT r.id,r."requestedById",r."sourceRevision",r."teamId",r."sourceId",
 r."destinationConnectionId",r."destinationDatabase",r."destinationSchema",r."streamId",r.binding,COALESCE(r."installationId",''),COALESCE(r."policyDigest",''),r."manifestDigest",
 COALESCE((SELECT i."approvedOrigin" FROM connector_installations i WHERE i.id=r."installationId" AND i."teamId"=r."teamId"),'')`+joins+`
 WHERE r.id=$1 AND r.status='QUEUED' AND `+liveCurrent()+` FOR UPDATE OF r SKIP LOCKED`, candidate).Scan(
		&lease.RunID, &lease.UserID, &lease.SourceRevision, &lease.Scope.TeamID, &lease.Scope.SourceID,
		&lease.Scope.ConnectionID, &lease.Scope.Database, &lease.Scope.Schema, &lease.Scope.Stream, &lease.Scope.Binding,
		&lease.InstallationID, &lease.PolicyDigest, &lease.ManifestDigest, &lease.ApprovedOrigin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lease.LeaseID = uuid.NewString()
	err = tx.QueryRowContext(ctx, `UPDATE connector_sync_runs SET status='RUNNING',"leaseId"=$2,
 "startedAt"=clock_timestamp() AT TIME ZONE 'UTC',"expiresAt"=(clock_timestamp() AT TIME ZONE 'UTC')+interval '15 minutes',
 "updatedAt"=clock_timestamp() AT TIME ZONE 'UTC' WHERE id=$1 RETURNING "expiresAt"`, lease.RunID, lease.LeaseID).Scan(&lease.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if err = audit(ctx, tx, lease.UserID, lease.RunID, "CLAIM"); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &lease, nil
}

// Active lets the supervisor cancel a container after user/source revocation.
// The bridge independently checks the same state on every destination operation.
func (s Store) Active(ctx context.Context, lease Lease) (bool, error) {
	var active bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1`+joins+` WHERE r.id=$1 AND r."leaseId"=$2
 AND r.status='RUNNING' AND `+liveCurrent()+`)`, lease.RunID, lease.LeaseID).Scan(&active)
	return active, err
}

// Succeed requires the worker's terminal sequence to match the bridge's durable
// receipt. A process exit alone is not successful synchronization.
func (s Store) Succeed(ctx context.Context, lease Lease, sequence int64) error {
	if sequence < 1 {
		return ErrLeaseUnavailable
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockInstallation(ctx, tx, lease.RunID); err != nil {
		return err
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT r.id`+joins+` WHERE r.id=$1 AND r."leaseId"=$2 AND r.status='RUNNING'
 AND (r."lastReceipt"->>'sequence')::bigint=$3 AND `+liveCurrent()+` FOR UPDATE OF r FOR SHARE OF s,c,tenant,t,u,m`, lease.RunID, lease.LeaseID, sequence).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLeaseUnavailable
	}
	if err != nil {
		return err
	}
	var fileInput bool
	if err = tx.QueryRowContext(ctx, `SELECT "fileHash" IS NOT NULL FROM connector_sync_runs WHERE id=$1`, id).Scan(&fileInput); err != nil {
		return err
	}
	if fileInput {
		var fileID string
		err = tx.QueryRowContext(ctx, `SELECT f.id FROM "File" f
 JOIN postgoose_storage_destinations d ON d.id=f."storageDestinationId" AND d."teamId"=f."teamId"
 JOIN tenants ft ON ft.id=d."tenantId" AND ft."teamId"=f."teamId"
 JOIN connector_sync_runs r ON r.id=$1 AND f.id=r."configurationSnapshot"->>'file_id' AND f."teamId"=r."teamId"
 WHERE f.deleted=false AND d.deleted=false AND ft.deleted=false AND ft.status='ACTIVE'
 AND f.size>0 AND f.size<=52428800 AND lower(f.name) LIKE '%.csv'
 FOR SHARE OF f,d,ft`, id).Scan(&fileID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLeaseUnavailable
		}
		if err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE connector_sync_runs SET status='SUCCEEDED',"finishedAt"=clock_timestamp() AT TIME ZONE 'UTC',
 "updatedAt"=clock_timestamp() AT TIME ZONE 'UTC',"leaseId"=NULL WHERE id=$1`, id); err != nil {
		return err
	}
	if err = audit(ctx, tx, lease.UserID, id, "SUCCEED"); err != nil {
		return err
	}
	return tx.Commit()
}

// Failure codes are controlled by the supervisor's constant vocabulary.
// Cancellation wins over late process completion/failure.
func (s Store) Fail(ctx context.Context, lease Lease, code string) error {
	switch code {
	case "worker_failed", "dispatch_failed", "worker_result_invalid", "authorization_changed":
	default:
		if !workerFailureCodes[code] {
			return errors.New("invalid connector failure code")
		}
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE connector_sync_runs SET status='FAILED',"failureCode"=$3,"leaseId"=NULL,
 "finishedAt"=clock_timestamp() AT TIME ZONE 'UTC',"updatedAt"=clock_timestamp() AT TIME ZONE 'UTC'
 WHERE id=$1 AND "leaseId"=$2 AND status='RUNNING' AND deleted=false`, lease.RunID, lease.LeaseID, code)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count > 0 {
		if err = audit(ctx, tx, lease.UserID, lease.RunID, "FAIL"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Expire reconciles bounded batches of abandoned leases/queued requests. It does
// not dispatch them again; a new explicit request resumes the durable source state.
func (s Store) Expire(ctx context.Context) (int64, error) {
	result, err := s.DB.ExecContext(ctx, `WITH stale AS (
 SELECT id FROM connector_sync_runs WHERE status IN ('QUEUED','RUNNING') AND deleted=false
 AND "expiresAt"<=(clock_timestamp() AT TIME ZONE 'UTC') ORDER BY "expiresAt" LIMIT 100 FOR UPDATE SKIP LOCKED
 ), expired AS (
 UPDATE connector_sync_runs r SET "failureCode"=CASE WHEN r.status='QUEUED' THEN 'queue_expired' ELSE 'lease_expired' END,
 status='FAILED',"leaseId"=NULL,"finishedAt"=clock_timestamp() AT TIME ZONE 'UTC',"updatedAt"=clock_timestamp() AT TIME ZONE 'UTC'
 FROM stale WHERE r.id=stale.id RETURNING r.id,r."requestedById"
 ) INSERT INTO postgoose_action_logs (id,"userId",action,query,"createdAt","updatedAt",deleted)
 SELECT gen_random_uuid()::text,"requestedById",'CONNECTOR_SYNC:EXPIRE','CONNECTOR_SYNC:'||id,
 clock_timestamp() AT TIME ZONE 'UTC',clock_timestamp() AT TIME ZONE 'UTC',false FROM expired`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
