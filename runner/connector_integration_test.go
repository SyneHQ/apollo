package runner

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/infisical/go-sdk/packages/models"
)

func TestConnectorContainerIsolation(t *testing.T) {
	image := os.Getenv("CONNECTOR_WORKER_TEST_IMAGE")
	if image == "" {
		t.Skip("CONNECTOR_WORKER_TEST_IMAGE requires the built local worker image")
	}
	r := NewLocalRunner(image, []models.Secret{{SecretKey: "DATABASE_URL", SecretValue: "database-canary"}, {SecretKey: "KMS_API_URL", SecretValue: "kms-canary"}})
	script := `import json,os
status=dict(line.split(':',1) for line in open('/proc/self/status') if ':' in line)
tmp=os.statvfs('/tmp')
print(json.dumps({'uid':os.getuid(),'readonly':bool(os.statvfs('/').f_flag & os.ST_RDONLY),'caps':status['CapEff'].strip(),'no_new_privs':status['NoNewPrivs'].strip(),'tmp_bytes':tmp.f_blocks*tmp.f_frsize,'platform_secrets':any(k in os.environ for k in ('DATABASE_URL','KMS_API_URL','APOLLO_JOB_SIGNING_KEY'))}))`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := r.RunJob(ctx, "/usr/local/bin/python", JobRequest{Name: "connector-sync-" + uuid.NewString(), Image: image, Command: "-c",
		Resources: Resources{CPU: "1", Memory: "512Mi"}, Overrides: &JobOverrides{Args: []string{script}}})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		UID             int    `json:"uid"`
		Readonly        bool   `json:"readonly"`
		Caps            string `json:"caps"`
		NoNewPrivs      string `json:"no_new_privs"`
		TmpBytes        int64  `json:"tmp_bytes"`
		PlatformSecrets bool   `json:"platform_secrets"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal("invalid isolation result", err)
	}
	if result.UID != 10001 || !result.Readonly || result.Caps != "0000000000000000" || result.NoNewPrivs != "1" || result.TmpBytes > 64<<20 || result.PlatformSecrets {
		t.Fatalf("container isolation mismatch: %+v", result)
	}
}
