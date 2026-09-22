package connectorjobs

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SyneHQ/apollo/runner"
)

type probeRunner struct {
	run func(context.Context, runner.JobRequest) (string, error)
}

func (p probeRunner) RunJob(ctx context.Context, _ string, r runner.JobRequest) (string, error) {
	return p.run(ctx, r)
}
func (p probeRunner) DeleteJob(context.Context, string) error              { return nil }
func (p probeRunner) UpdateSchedule(context.Context, string, string) error { return nil }
func options() Options {
	return Options{Image: "sha256:" + strings.Repeat("a", 64), BootstrapOrigin: "https://app.example", BridgeOrigin: "https://bridge.example", SigningKey: "disposable-connector-job-signing-key-32-bytes"}
}

func TestSupervisorRejectsMutableImagesAndUntrustedOrigins(t *testing.T) {
	for _, image := range []string{"worker:latest", "worker:main", "", "sha256:short"} {
		if immutableImage.MatchString(image) {
			t.Fatal("mutable image", image)
		}
	}
	for _, origin := range []string{"http://app.example", "https://user:pass@app.example", "https://app.example/path", "https://app.example?key=a", "https://app.example:99999", "https://app.example:0"} {
		if validOrigin(origin) {
			t.Fatal("invalid service origin", origin)
		}
	}
	if !immutableImage.MatchString("ghcr.io/synehq/connector-worker@sha256:"+strings.Repeat("a", 64)) || !validOrigin("https://bridge.example:8443") {
		t.Fatal("valid pin/origin rejected")
	}
}

func TestSupervisorRequiresTerminalJSON(t *testing.T) {
	for _, output := range []string{`{}`, `{"status":"succeeded","run_id":"r","sequence":1,"done":false}`, `{"status":"succeeded","run_id":"other","sequence":1,"done":true}`,
		`{"status":"succeeded","run_id":"r","sequence":1,"done":true,"secret":"x"}`, `{"status":"succeeded","run_id":"r","sequence":1,"done":true} {}`, strings.Repeat("x", 4097)} {
		if _, err := terminalSequence(output, "r"); err == nil {
			t.Fatal("invalid result accepted")
		}
	}
}

func TestSupervisorExecutesWithScopedTokensAndDurableReceipt(t *testing.T) {
	s, id, _, _ := fixture(t)
	ctx := context.Background()
	lease, err := s.Claim(ctx)
	if err != nil || lease == nil {
		t.Fatal(err)
	}
	o := options()
	probe := probeRunner{run: func(_ context.Context, r runner.JobRequest) (string, error) {
		if r.Name != "connector-sync-"+id || r.Command != "-m" || r.ArgsJSONBase64 != "" || len(r.Overrides.Args) != 1 || r.Overrides.Args[0] != "syne_connectors.worker" {
			t.Fatal("arbitrary worker command")
		}
		for _, variable := range r.Overrides.Env {
			if strings.Contains(variable.Value, o.SigningKey) {
				t.Fatal("worker received signing key")
			}
			if !strings.HasSuffix(variable.Name, "_TOKEN") {
				continue
			}
			parts := strings.Split(variable.Value, ".")
			if len(parts) != 3 {
				t.Fatal("malformed grant")
			}
			mac := hmac.New(sha256.New, []byte(o.SigningKey))
			mac.Write([]byte(parts[0] + "." + parts[1]))
			if base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) != parts[2] {
				t.Fatal("grant signature mismatch")
			}
			raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var claims map[string]any
			if err := json.Unmarshal(raw, &claims); err != nil {
				t.Fatal(err)
			}
			expected := "syne-ingestion"
			if variable.Name == "CONNECTOR_BOOTSTRAP_TOKEN" {
				expected = "syne-connector-bootstrap"
			}
			if claims["aud"] != expected || claims["run_id"] != id || claims["lease_id"] != lease.LeaseID || claims["connections"] != nil {
				t.Fatal("widened token")
			}
			if claims["exp"].(float64)-claims["iat"].(float64) > 900 {
				t.Fatal("long-lived grant")
			}
		}
		_, err := s.DB.Exec(`UPDATE connector_sync_runs SET "lastReceipt"='{"sequence":2}' WHERE id=$1`, id)
		if err != nil {
			t.Fatal(err)
		}
		return `{"status":"succeeded","run_id":"` + id + `","sequence":2,"records_committed":3,"done":true}`, nil
	}}
	supervisor, err := NewSupervisor(s, probe, o)
	if err != nil {
		t.Fatal(err)
	}
	supervisor.execute(ctx, *lease)
	var status string
	if err := s.DB.QueryRow(`SELECT status FROM connector_sync_runs WHERE id=$1`, id).Scan(&status); err != nil || status != "SUCCEEDED" {
		t.Fatal(status, err)
	}
}

func TestSupervisorStopsOnCancellationWithoutOverwritingIt(t *testing.T) {
	s, id, _, _ := fixture(t)
	lease, err := s.Claim(context.Background())
	if err != nil || lease == nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan struct{})
	probe := probeRunner{run: func(ctx context.Context, _ runner.JobRequest) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	supervisor, err := NewSupervisor(s, probe, options())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() { defer close(done); supervisor.execute(ctx, *lease) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("worker did not start")
	}
	if _, err := s.DB.Exec(`UPDATE connector_sync_runs SET status='CANCELLED',"leaseId"=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("worker did not cancel")
	}
	var status string
	if err := s.DB.QueryRow(`SELECT status FROM connector_sync_runs WHERE id=$1`, id).Scan(&status); err != nil || status != "CANCELLED" {
		t.Fatal("cancel overwritten", status, err)
	}
}

func TestSupervisorDiscardsUntrustedRunnerErrorBodies(t *testing.T) {
	s, id, _, _ := fixture(t)
	lease, err := s.Claim(context.Background())
	if err != nil || lease == nil {
		t.Fatal(err)
	}
	probe := probeRunner{run: func(context.Context, runner.JobRequest) (string, error) {
		return "secret-output", errors.New("secret-provider-response")
	}}
	supervisor, err := NewSupervisor(s, probe, options())
	if err != nil {
		t.Fatal(err)
	}
	supervisor.execute(context.Background(), *lease)
	var code string
	if err := s.DB.QueryRow(`SELECT "failureCode" FROM connector_sync_runs WHERE id=$1`, id).Scan(&code); err != nil || code != "worker_failed" {
		t.Fatal(code, err)
	}
}
