package connectorjobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func privateFixture(t *testing.T) (Store, string, string) {
	t.Helper()
	s, builtin, user, source := fixture(t)
	installation, privateSource, run := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := s.DB.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE connector_sync_runs SET status='CANCELLED' WHERE id=$1`, builtin)
	exec(`INSERT INTO connector_installations(id,"teamId","installedById","manifestId","manifestVersion","manifestDigest","packageDigest","policyDigest","approvedOrigin",package)
 SELECT $1,"teamId",$2,'acme/payments','1.0.0',$3,$3,$4,'https://api.acme.example','{}' FROM connector_sources WHERE id=$5`, installation, user, strings.Repeat("a", 64), strings.Repeat("b", 64), source)
	exec(`INSERT INTO connector_sources(id,"teamId",name,"manifestId","manifestVersion","manifestDigest",configuration,"encryptedSecrets","secretFields","destinationConnectionId","destinationDatabase","destinationSchema","updatedAt","installationId","policyDigest")
 SELECT $1,"teamId",'private fixture','acme/payments','1.0.0',"manifestDigest",configuration,"encryptedSecrets","secretFields","destinationConnectionId","destinationDatabase","destinationSchema",now(),$2,$3 FROM connector_sources WHERE id=$4`, privateSource, installation, strings.Repeat("b", 64), source)
	exec(`INSERT INTO connector_sync_runs(id,"teamId","sourceId","sourceRevision","requestedById","streamId","manifestId","manifestVersion","manifestDigest","configurationSnapshot",binding,"destinationConnectionId","destinationDatabase","destinationSchema","expiresAt","updatedAt","installationId","policyDigest")
 SELECT $1,"teamId",id,revision,$2,'settlements',"manifestId","manifestVersion","manifestDigest",configuration,$3,"destinationConnectionId","destinationDatabase","destinationSchema",(now() AT TIME ZONE 'UTC')+interval '30 minutes',now(),"installationId","policyDigest" FROM connector_sources WHERE id=$4`, run, user, strings.Repeat("c", 64), privateSource)
	t.Cleanup(func() { s.DB.Exec(`UPDATE connector_installations SET "revokedAt"=now() WHERE id=$1`, installation) })
	return s, run, installation
}

func TestPrivateAdmissionFlagAndRevocationStopClaimActiveAndSuccess(t *testing.T) {
	s, run, installation := privateFixture(t)
	ctx := context.Background()
	t.Setenv("CONNECTOR_PRIVATE_SYNCS_ENABLED", "")
	if lease, err := s.Claim(ctx); err != nil || lease != nil {
		t.Fatal("private run admitted while disabled", err)
	}
	t.Setenv("CONNECTOR_PRIVATE_SYNCS_ENABLED", "true")
	lease, err := s.Claim(ctx)
	if err != nil || lease == nil || lease.RunID != run || lease.InstallationID != installation || lease.ApprovedOrigin != "https://api.acme.example" {
		t.Fatal("private claim unavailable", err)
	}
	if active, err := s.Active(ctx, *lease); err != nil || !active {
		t.Fatal("private live lease unavailable", err)
	}
	t.Setenv("CONNECTOR_PRIVATE_SYNCS_ENABLED", "false")
	if active, err := s.Active(ctx, *lease); err != nil || active {
		t.Fatal("disabled private lease active", err)
	}
	t.Setenv("CONNECTOR_PRIVATE_SYNCS_ENABLED", "true")
	if _, err := s.DB.Exec(`UPDATE connector_sync_runs SET "lastReceipt"='{"sequence":1}' WHERE id=$1`, run); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE connector_installations SET "revokedAt"=now() WHERE id=$1`, installation); err != nil {
		t.Fatal(err)
	}
	if active, err := s.Active(ctx, *lease); err != nil || active {
		t.Fatal("revoked installation still active", err)
	}
	if err := s.Succeed(ctx, *lease, 1); !errors.Is(err, ErrLeaseUnavailable) {
		t.Fatal("revoked installation succeeded", err)
	}
}

func TestRevokedPrivateInstallationCannotBeClaimed(t *testing.T) {
	s, _, installation := privateFixture(t)
	t.Setenv("CONNECTOR_PRIVATE_SYNCS_ENABLED", "true")
	if _, err := s.DB.Exec(`UPDATE connector_installations SET "revokedAt"=now() WHERE id=$1`, installation); err != nil {
		t.Fatal(err)
	}
	if lease, err := s.Claim(context.Background()); err != nil || lease != nil {
		t.Fatal("revoked installation claimed", err)
	}
}

func TestPrivateSupervisorHandoffContainsOnlyTrustedIdentity(t *testing.T) {
	s := Supervisor{options: options()}
	lease := Lease{RunID: uuid.NewString(), LeaseID: uuid.NewString(), UserID: "user", SourceRevision: 1, ExpiresAt: time.Now().Add(time.Minute),
		Scope: Scope{TeamID: "team", Binding: strings.Repeat("c", 64)}, InstallationID: uuid.NewString(), PolicyDigest: strings.Repeat("b", 64), ManifestDigest: strings.Repeat("a", 64), ApprovedOrigin: "https://api.acme.example"}
	t.Setenv("CONNECTOR_PRIVATE_SYNCS_ENABLED", "")
	if _, err := s.request(lease); !errors.Is(err, ErrLeaseUnavailable) {
		t.Fatal("private dispatch while disabled", err)
	}
	t.Setenv("CONNECTOR_PRIVATE_SYNCS_ENABLED", "true")
	request, err := s.request(lease)
	if err != nil {
		t.Fatal(err)
	}
	var trust map[string]string
	for _, entry := range request.Overrides.Env {
		if entry.Name == "CONNECTOR_PRIVATE_INSTALLATION" {
			if err := json.Unmarshal([]byte(entry.Value), &trust); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(trust) != 6 || trust["binding"] != lease.Scope.Binding || trust["team_id"] != lease.Scope.TeamID || trust["id"] != lease.InstallationID || trust["policy_digest"] != lease.PolicyDigest || trust["manifest_digest"] != lease.ManifestDigest || trust["approved_origin"] != lease.ApprovedOrigin {
		t.Fatal("private trusted handoff mismatch")
	}
	lease.InstallationID = ""
	lease.PolicyDigest = ""
	lease.ApprovedOrigin = ""
	request, err = s.request(lease)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range request.Overrides.Env {
		if strings.HasPrefix(entry.Name, "CONNECTOR_PRIVATE_") {
			t.Fatal("builtin received private authority")
		}
	}
}
