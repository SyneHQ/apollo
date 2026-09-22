package connectorjobs

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SyneHQ/apollo/internal/jobtoken"
	"github.com/SyneHQ/apollo/runner"
	"github.com/google/uuid"
)

type Options struct {
	Image, BootstrapOrigin, BridgeOrigin, SigningKey, ServiceCA string
}

var immutableImage = regexp.MustCompile(`^(?:[a-zA-Z0-9][a-zA-Z0-9./:_-]*@)?sha256:[a-f0-9]{64}$`)

func validOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || len(origin) > 2048 {
		return false
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return false
		}
	}
	return true
}

type Supervisor struct {
	store   Store
	runner  runner.Runner
	options Options
}

func NewSupervisor(store Store, r runner.Runner, options Options) (*Supervisor, error) {
	if store.DB == nil || r == nil || !immutableImage.MatchString(options.Image) || len(options.SigningKey) < 32 ||
		!validOrigin(options.BootstrapOrigin) || !validOrigin(options.BridgeOrigin) || len(options.ServiceCA) > 65536 {
		return nil, errors.New("connector supervisor requires immutable image, HTTPS origins and job signing key")
	}
	if options.ServiceCA != "" && !x509.NewCertPool().AppendCertsFromPEM([]byte(options.ServiceCA)) {
		return nil, errors.New("invalid connector service CA")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := store.DB.ExecContext(ctx, `SELECT "leaseId","lastReceipt" FROM connector_sync_runs LIMIT 0`); err != nil {
		return nil, errors.New("connector run migration required")
	}
	return &Supervisor{store: store, runner: r, options: options}, nil
}

// Run bounds local concurrency to two. The durable app queue supplies workspace
// admission; no in-memory submission is necessary and no run is blindly replayed.
func (s *Supervisor) Run(ctx context.Context) {
	slots := make(chan struct{}, 2)
	var wg sync.WaitGroup
	defer wg.Wait()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		operation, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := s.store.Expire(operation)
		cancel()
		if err != nil {
			log.Print("connector expiry reconciliation unavailable")
			continue
		}
		for len(slots) < cap(slots) {
			operation, cancel := context.WithTimeout(ctx, 5*time.Second)
			lease, err := s.store.Claim(operation)
			cancel()
			if err != nil {
				log.Print("connector queue claim unavailable")
				break
			}
			if lease == nil {
				break
			}
			slots <- struct{}{}
			wg.Add(1)
			go func(lease Lease) { defer wg.Done(); defer func() { <-slots }(); s.execute(ctx, lease) }(*lease)
		}
	}
}

func (s *Supervisor) request(lease Lease) (runner.JobRequest, error) {
	now := time.Now().Unix()
	expires := min(lease.ExpiresAt.Unix(), now+900)
	if expires <= now {
		return runner.JobRequest{}, ErrLeaseUnavailable
	}
	makeToken := func(audience string) (string, error) {
		claims := map[string]any{"iss": "syne-apollo", "aud": audience, "sub": lease.UserID, "iat": now, "exp": expires, "jti": uuid.NewString(),
			"run_id": lease.RunID, "lease_id": lease.LeaseID, "source_revision": lease.SourceRevision, "scope": lease.Scope}
		if audience == "syne-ingestion" {
			claims["allow_install"] = true
		}
		return jobtoken.Sign([]byte(s.options.SigningKey), claims)
	}
	bootstrap, err := makeToken("syne-connector-bootstrap")
	if err != nil {
		return runner.JobRequest{}, err
	}
	ingestion, err := makeToken("syne-ingestion")
	if err != nil {
		return runner.JobRequest{}, err
	}
	deadline, _ := json.Marshal(expires)
	env := []runner.EnvVar{{Name: "CONNECTOR_RUN_ID", Value: lease.RunID}, {Name: "CONNECTOR_DEADLINE_EPOCH", Value: string(deadline)},
		{Name: "CONNECTOR_BOOTSTRAP_ORIGIN", Value: s.options.BootstrapOrigin}, {Name: "CONNECTOR_BOOTSTRAP_TOKEN", Value: bootstrap},
		{Name: "CONNECTOR_BRIDGE_ORIGIN", Value: s.options.BridgeOrigin}, {Name: "CONNECTOR_INGESTION_TOKEN", Value: ingestion}}
	if s.options.ServiceCA != "" {
		env = append(env, runner.EnvVar{Name: "CONNECTOR_SERVICE_CA_PEM", Value: s.options.ServiceCA})
	}
	return runner.JobRequest{Name: "connector-sync-" + lease.RunID, JobID: lease.RunID, AuthorizedUser: lease.UserID, Image: s.options.Image,
		Prefix: "/usr/local/bin/python", Command: "-m", Type: runner.JobTypeOneTime, Resources: runner.Resources{CPU: "1", Memory: "512Mi"},
		Overrides: &runner.JobOverrides{Args: []string{"syne_connectors.worker"}, Env: env}}, nil
}

func terminalSequence(output string, runID string) (int64, error) {
	if len(output) > 4096 {
		return 0, errors.New("invalid worker result")
	}
	var result struct {
		Status   string `json:"status"`
		RunID    string `json:"run_id"`
		Sequence int64  `json:"sequence"`
		Records  int64  `json:"records_committed"`
		Done     bool   `json:"done"`
	}
	d := json.NewDecoder(strings.NewReader(output))
	d.DisallowUnknownFields()
	if err := d.Decode(&result); err != nil {
		return 0, errors.New("invalid worker result")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF || result.Status != "succeeded" || result.RunID != runID || !result.Done || result.Sequence < 1 || result.Records < 0 {
		return 0, errors.New("invalid worker result")
	}
	return result.Sequence, nil
}

func (s *Supervisor) execute(parent context.Context, lease Lease) {
	ctx, cancel := context.WithDeadline(parent, lease.ExpiresAt)
	defer cancel()
	request, err := s.request(lease)
	if err != nil {
		s.fail(lease, "dispatch_failed")
		return
	}
	active, err := s.store.Active(ctx, lease)
	if err != nil || !active {
		s.fail(lease, "authorization_changed")
		return
	}
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				probe, stop := context.WithTimeout(ctx, 3*time.Second)
				active, err := s.store.Active(probe, lease)
				stop()
				if err != nil || !active {
					cancel()
					return
				}
			}
		}
	}()
	output, runErr := s.runner.RunJob(ctx, request.Prefix, request)
	cancel()
	<-monitorDone
	if code := workerFailure(output, lease.RunID); code != "" {
		s.fail(lease, code)
		return
	}
	if runErr != nil {
		s.fail(lease, "worker_failed")
		return
	}
	sequence, err := terminalSequence(output, lease.RunID)
	if err != nil {
		s.fail(lease, "worker_result_invalid")
		return
	}
	finish, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := s.store.Succeed(finish, lease, sequence); err != nil {
		// A metadata commit may have succeeded despite a lost acknowledgement.
		// Fail only updates a still-running matching lease, never terminal state.
		s.fail(lease, "authorization_changed")
	}
}

func (s *Supervisor) fail(lease Lease, code string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.store.Fail(ctx, lease, code); err != nil {
		log.Print("connector terminal state unavailable; lease expiry will reconcile")
	}
}
