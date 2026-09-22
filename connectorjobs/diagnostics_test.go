package connectorjobs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SyneHQ/apollo/runner"
)

func TestWorkerDiagnosticsRejectUntrustedOutput(t *testing.T) {
	valid := `{"status":"failed","run_id":"r","error":"source_access_required"}`
	if workerFailure(valid, "r") != "source_access_required" {
		t.Fatal("valid diagnostic rejected")
	}
	for _, output := range []string{valid, `{}`, strings.Replace(valid, "failed", "succeeded", 1),
		strings.Replace(valid, "source_access_required", "secret-canary", 1), valid + `{}`,
		strings.TrimSuffix(valid, "}") + `,"details":"secret-canary"}`, strings.Repeat("x", 513)} {
		run := "r"
		if output == valid {
			run = "other-run"
		}
		if workerFailure(output, run) != "" {
			t.Fatal("untrusted output accepted")
		}
	}
}

func TestSupervisorPersistsOnlyAllowlistedRunDiagnostics(t *testing.T) {
	for _, code := range []string{"source_changed", "secret-canary"} {
		t.Run(code, func(t *testing.T) {
			s, id, _, _ := fixture(t)
			lease, err := s.Claim(context.Background())
			if err != nil || lease == nil {
				t.Fatal(err)
			}
			probe := probeRunner{run: func(context.Context, runner.JobRequest) (string, error) {
				return `{"status":"failed","run_id":"` + id + `","error":"` + code + `"}`, errors.New("secret-canary from runner")
			}}
			supervisor, err := NewSupervisor(s, probe, options())
			if err != nil {
				t.Fatal(err)
			}
			supervisor.execute(context.Background(), *lease)
			var status, failure string
			if err := s.DB.QueryRow(`SELECT status,"failureCode" FROM connector_sync_runs WHERE id=$1`, id).Scan(&status, &failure); err != nil {
				t.Fatal(err)
			}
			expected := code
			if code == "secret-canary" {
				expected = "worker_failed"
			}
			if status != "FAILED" || failure != expected {
				t.Fatal(status, failure)
			}
		})
	}
}
