package runner

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	config "github.com/SyneHQ/apollo"
)

func newHakopodHarness(t *testing.T, handler http.HandlerFunc) (*HakopodRunner, JobRequest) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	cfg := config.HakopodConfig{APIURL: server.URL, ApplicationID: strings.Repeat("a", 32), Revision: 7, Templates: map[string]config.HakopodTemplate{"handleBackupJob": {Service: "backup-job", Image: "ghcr.io/example/worker@sha256:" + strings.Repeat("b", 64), Prefix: "/app/rover", CPU: "1000m", Memory: "2Gi"}}}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	r, err := NewHakopodRunner(cfg, strings.Repeat("k", 32), string(ca))
	if err != nil {
		t.Fatal(err)
	}
	r.pollInterval = time.Millisecond
	req := JobRequest{OwnerScope: "team-a", Name: "backup-a", JobID: "backup-a-unique", Command: "handleBackupJob", Image: cfg.Templates["handleBackupJob"].Image, Prefix: "/app/rover", Resources: Resources{CPU: "1000m", Memory: "2Gi"}, ArgsJSONBase64: "eyJiYWNrdXBTY2hlZHVsZUlkIjoiYSJ9"}
	return r, req
}
func testInvocation(r *HakopodRunner, req JobRequest, status string) invocationRecord {
	t := r.config.Templates[req.Command]
	return invocationRecord{ID: "run-one", ApplicationID: r.config.ApplicationID, Service: t.Service, Revision: r.config.Revision, Image: t.Image, CorrelationID: req.Name, Status: status}
}

func TestHakopodUsesOnlyReviewedTemplateInputs(t *testing.T) {
	var r *HakopodRunner
	var req JobRequest
	var calls atomic.Int32
	r, req = newHakopodHarness(t, func(w http.ResponseWriter, h *http.Request) {
		calls.Add(1)
		if h.Header.Get("Authorization") != "Bearer "+strings.Repeat("k", 32) || h.Header.Get("X-Hakopod-Owner-Scope") != "team-a" {
			t.Error("missing scoped authentication")
		}
		if strings.HasSuffix(h.URL.Path, "/logs") {
			json.NewEncoder(w).Encode(map[string]any{"text": "done", "truncated": false})
			return
		}
		if h.Method == http.MethodPost {
			if h.Header.Get("Idempotency-Key") != invocationKey(req.JobID) {
				t.Error("idempotency key missing")
			}
			var body map[string]json.RawMessage
			json.NewDecoder(h.Body).Decode(&body)
			if len(body) != 5 || body["expected_image"] == nil || body["expected_revision"] == nil || body["inputs"] == nil || body["owner_scope"] == nil || body["correlation_id"] == nil {
				t.Error("unexpected invocation fields")
			}
			var create invocationCreate
			raw, _ := json.Marshal(body)
			json.Unmarshal(raw, &create)
			if create.ExpectedImage != req.Image || create.ExpectedRevision != 7 || create.Inputs["payload"] != req.ArgsJSONBase64 || len(create.Inputs) != 1 {
				t.Error("reviewed binding or payload was changed")
			}
		}
		json.NewEncoder(w).Encode(testInvocation(r, req, "succeeded"))
	})
	logs, err := r.RunJob(context.Background(), req.Prefix, req)
	if err != nil || logs != "done" || calls.Load() != 2 {
		t.Fatalf("unexpected result %q %v calls=%d", logs, err, calls.Load())
	}
	before := calls.Load()
	for _, change := range []func(*JobRequest){func(q *JobRequest) { q.OwnerScope = "" }, func(q *JobRequest) { q.Image = "alpine:latest" }, func(q *JobRequest) { q.Overrides = &JobOverrides{Env: []EnvVar{{Name: "PATH", Value: "/bad"}}} }, func(q *JobRequest) { q.Resources.Memory = "8Gi" }, func(q *JobRequest) { q.Command = "sh" }} {
		invalid := req
		change(&invalid)
		if _, err := r.RunJob(context.Background(), invalid.Prefix, invalid); err == nil {
			t.Fatal("unsafe request accepted")
		}
	}
	if calls.Load() != before {
		t.Fatal("unsafe request reached the API")
	}
}

func TestHakopodRecoversLostAcknowledgementWithoutRecreatingJob(t *testing.T) {
	var r *HakopodRunner
	var req JobRequest
	var creates, cancels, lookups atomic.Int32
	r, req = newHakopodHarness(t, func(w http.ResponseWriter, h *http.Request) {
		switch {
		case strings.HasSuffix(h.URL.Path, "/cancel"):
			cancels.Add(1)
			json.NewEncoder(w).Encode(testInvocation(r, req, "cancelled"))
		case h.Method == http.MethodPost:
			creates.Add(1)
			io.Copy(io.Discard, h.Body)
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			connection.Close()
		default:
			lookups.Add(1)
			if h.Header.Get("Idempotency-Key") != invocationKey(req.JobID) || h.URL.Query().Get("correlation_id") != req.Name {
				t.Error("recovery not bound to original request")
			}
			json.NewEncoder(w).Encode(map[string]any{"items": []invocationRecord{testInvocation(r, req, "running")}})
		}
	})
	if _, err := r.RunJob(context.Background(), req.Prefix, req); err == nil {
		t.Fatal("lost acknowledgement must report failure")
	}
	if creates.Load() != 1 || lookups.Load() != 1 || cancels.Load() != 1 {
		t.Fatalf("unexpected recovery calls: create%d lookup%d cancel%d", creates.Load(), lookups.Load(), cancels.Load())
	}
}

func TestHakopodCancellationAndRestartLookup(t *testing.T) {
	var r *HakopodRunner
	var req JobRequest
	var cancels atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, req = newHakopodHarness(t, func(w http.ResponseWriter, h *http.Request) {
		if strings.HasSuffix(h.URL.Path, "/cancel") {
			cancels.Add(1)
			json.NewEncoder(w).Encode(testInvocation(r, req, "cancelled"))
			return
		}
		if h.Method == http.MethodGet && h.URL.Query().Get("correlation_id") != "" {
			if h.URL.Query().Get("active") != "true" {
				t.Error("delete must select active invocations")
			}
			json.NewEncoder(w).Encode(map[string]any{"items": []invocationRecord{testInvocation(r, req, "running")}})
			return
		}
		json.NewEncoder(w).Encode(testInvocation(r, req, "running"))
		if h.Method == http.MethodGet {
			cancel()
		}
	})
	if _, err := r.RunJob(ctx, req.Prefix, req); err == nil {
		t.Fatal("cancelled execution returned success")
	}
	// This path uses durable API records, not an in-memory submitted-job map.
	if err := r.DeleteJob(WithOwnerScope(context.Background(), "team-a"), req.Name); err != nil {
		t.Fatal(err)
	}
	if cancels.Load() != 2 {
		t.Fatalf("expected two cancellation requests, got%d", cancels.Load())
	}
	if err := r.DeleteJob(context.Background(), req.Name); err == nil {
		t.Fatal("cancellation without verified owner accepted")
	}
}

func TestHakopodRejectsMismatchedReceiptsAndOversizedLogs(t *testing.T) {
	for _, kind := range []string{"image", "revision", "logs"} {
		t.Run(kind, func(t *testing.T) {
			var r *HakopodRunner
			var req JobRequest
			r, req = newHakopodHarness(t, func(w http.ResponseWriter, h *http.Request) {
				if strings.HasSuffix(h.URL.Path, "/logs") {
					json.NewEncoder(w).Encode(map[string]any{"text": strings.Repeat("x", maxInvocationLogs+1)})
					return
				}
				value := testInvocation(r, req, "succeeded")
				if kind == "image" {
					value.Image = "other"
				}
				if kind == "revision" {
					value.Revision++
				}
				json.NewEncoder(w).Encode(value)
			})
			if _, err := r.RunJob(context.Background(), req.Prefix, req); err == nil {
				t.Fatal("invalid receipt accepted")
			}
		})
	}
}

func TestHakopodRejectsRedirectsAndRedactsAPIErrorBodies(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var targetCalls atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, h *http.Request) { targetCalls.Add(1) }))
			defer target.Close()
			r, req := newHakopodHarness(t, func(w http.ResponseWriter, h *http.Request) {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				w.Write([]byte("private-provider-secret"))
			})
			_, err := r.RunJob(context.Background(), req.Prefix, req)
			if err == nil || strings.Contains(err.Error(), "private-provider-secret") || targetCalls.Load() != 0 {
				t.Fatalf("unsafe error or redirect: %v calls%d", err, targetCalls.Load())
			}
		})
	}
}

func TestHakopodAliasUsesPinnedImageAndFixedResourceCeiling(t *testing.T) {
	var r *HakopodRunner
	var req JobRequest
	r, req = newHakopodHarness(t, func(w http.ResponseWriter, h *http.Request) {
		if strings.HasSuffix(h.URL.Path, "/logs") {
			json.NewEncoder(w).Encode(map[string]any{"text": "done"})
			return
		}
		var body invocationCreate
		json.NewDecoder(h.Body).Decode(&body)
		if body.ExpectedImage == req.Image || !strings.Contains(body.ExpectedImage, "@sha256:") {
			t.Error("alias was used as execution image")
		}
		json.NewEncoder(w).Encode(testInvocation(r, req, "succeeded"))
	})
	binding := r.config.Templates[req.Command]
	binding.RequestImage = "ghcr.io/example/worker:sudo"
	r.config.Templates[req.Command] = binding
	req.Image = binding.RequestImage
	req.Resources = Resources{CPU: "500m", Memory: "1Gi"}
	if _, err := r.RunJob(context.Background(), req.Prefix, req); err != nil {
		t.Fatal(err)
	}
}
