package runner

import (
	"github.com/infisical/go-sdk/packages/models"
	"strings"
	"testing"
)

func TestFlowrCannotReceivePlatformSecrets(t *testing.T) {
	r := &LocalRunner{Secrets: []models.Secret{{SecretKey: "DATABASE_URL", SecretValue: "database-canary"}, {SecretKey: "FLOWR_API_KEY", SecretValue: "admin-canary"}, {SecretKey: "APOLLO_JOB_SIGNING_KEY", SecretValue: "signer-canary"}, {SecretKey: "FLOWR_API_URL", SecretValue: "https://bridge"}}}
	env := strings.Join(r.buildEnvVars(JobRequest{Name: "flowr-pipeline-p", Overrides: &JobOverrides{Env: []EnvVar{{Name: "FLOWR_READ_JOB_TOKEN", Value: "scoped-token"}}}}), "\n")
	if strings.Contains(env, "canary") || !strings.Contains(env, "scoped-token") || !strings.Contains(env, "https://bridge") {
		t.Fatalf("incorrect secret policy")
	}
}
func TestResourcesBoundedAndContainerNamesScoped(t *testing.T) {
	l := &LocalRunner{}
	if _, err := l.buildResourceLimits(JobRequest{Resources: Resources{CPU: "100", Memory: "500Gi"}}); err == nil {
		t.Fatal("unbounded resources")
	}
	if containerName("customer-container") == "customer-container" || !strings.HasPrefix(containerName("x"), "apollo-") {
		t.Fatal("unscoped container name")
	}
}

func TestRoverKMSCapabilitiesExcludeAdministrationAndDevelopmentOverrides(t *testing.T) {
	runner := &LocalRunner{Secrets: []models.Secret{{SecretKey: "SYNE_KMS_SERVICE_TOKEN", SecretValue: "allowed"}, {SecretKey: "SYNE_KMS_ADMIN_TOKEN", SecretValue: "admin-canary"}, {SecretKey: "NODE_ENV", SecretValue: "development"}, {SecretKey: "SYNE_KMS_CA_PEM", SecretValue: "trusted-ca"}}}
	env := strings.Join(runner.buildEnvVars(JobRequest{Name: "backup-job"}), "\n")
	if !strings.Contains(env, "SYNE_KMS_SERVICE_TOKEN=allowed") || !strings.Contains(env, "SYNE_KMS_CA_PEM=trusted-ca") || strings.Contains(env, "admin-canary") || strings.Contains(env, "NODE_ENV") {
		t.Fatal("Rover secret isolation failed")
	}
}
