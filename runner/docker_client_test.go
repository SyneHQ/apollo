package runner

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/moby/moby/api/types/container"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMaintainedDockerClientExecutesConstrainedJob(t *testing.T) {
	var created, removed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1.47")
		switch {
		case path == "/_ping":
			w.Header().Set("API-Version", "1.47")
			fmt.Fprint(w, "OK")
		case path == "/images/test-image/json":
			fmt.Fprint(w, `{"Id":"image"}`)
		case path == "/containers/create":
			var body container.CreateRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if body.Config.Labels["syne.apollo.managed"] != "true" || r.URL.Query().Get("name") != containerName("flowr-pipeline-a") {
				t.Error("missing management boundary")
			}
			if body.HostConfig.PidsLimit == nil || *body.HostConfig.PidsLimit != 256 || len(body.HostConfig.CapDrop) != 1 || body.HostConfig.CapDrop[0] != "ALL" {
				t.Error("missing resource restrictions")
			}
			created.Store(true)
			w.WriteHeader(201)
			fmt.Fprint(w, `{"Id":"job-container"}`)
		case path == "/containers/job-container/start":
			w.WriteHeader(204)
		case path == "/containers/job-container/wait":
			fmt.Fprint(w, `{"StatusCode":0}`)
		case path == "/containers/job-container/logs":
			header := make([]byte, 8)
			header[0] = 1
			binary.BigEndian.PutUint32(header[4:], 4)
			w.Write(header)
			fmt.Fprint(w, "done")
		case path == "/containers/job-container" && r.Method == "DELETE":
			removed.Store(true)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected Docker operation %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(server.URL, "http://"))
	t.Setenv("DOCKER_API_VERSION", "1.47")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	runner := NewLocalRunner("test-image", nil)
	logs, err := runner.RunJob(context.Background(), "python", JobRequest{Name: "flowr-pipeline-a", Command: "run", Resources: Resources{CPU: "1", Memory: "512Mi"}})
	if err != nil || logs != "done" || !created.Load() || !removed.Load() {
		t.Fatalf("job lifecycle failed: %q %v created=%v removed=%v", logs, err, created.Load(), removed.Load())
	}
}
