// Package: analytics_job
//
// This example demonstrates how to programmatically trigger the analytics container
// (synehq/analytics:latest) as a reusable job, passing runtime parameters and environment
// variables, similar to how the build.sh script does for local testing.
//
// The job will run the analytics command with arguments and environment variables
// (e.g., EXECUTION_ID, USER_ID, DATABASE_PATH, API_KEY, REDIS_URL, LOG_LEVEL).
// See examples/analytics-container/build.sh for the equivalent shell invocation.

package main

import (
	"context"
	"fmt"
	"log"

	"github.com/SyneHQ/apollo/runner"
	"github.com/infisical/go-sdk/packages/models"
)

// AnalyticsParams holds parameters for the analytics job execution.
type AnalyticsParams struct {
	QueryType    string
	Database     string
	OutputFormat string
	Parallelism  int
	ExecutionID  string
	UserID       string
	TaskCount    int32
	DatabasePath string
	APIKey       string
	RedisURL     string
	LogLevel     string
}

// executeAnalyticsJob runs the analytics container as a reusable job, passing runtime args/env.
func executeAnalyticsJob(ctx context.Context, client runner.Runner, params AnalyticsParams) error {
	// Compose command-line arguments
	args := []string{
		"--query-type", params.QueryType,
		"--database", params.Database,
		"--output-format", params.OutputFormat,
		"--parallelism", fmt.Sprintf("%d", params.Parallelism),
	}

	// Compose environment variables (matching build.sh test invocation)
	env := []runner.EnvVar{
		{Name: "EXECUTION_ID", Value: params.ExecutionID},
		{Name: "USER_ID", Value: params.UserID},
	}
	if params.DatabasePath != "" {
		env = append(env, runner.EnvVar{Name: "DATABASE_PATH", Value: params.DatabasePath})
	}
	if params.APIKey != "" {
		env = append(env, runner.EnvVar{Name: "API_KEY", Value: params.APIKey})
	}
	if params.RedisURL != "" {
		env = append(env, runner.EnvVar{Name: "REDIS_URL", Value: params.RedisURL})
	}
	if params.LogLevel != "" {
		env = append(env, runner.EnvVar{Name: "LOG_LEVEL", Value: params.LogLevel})
	}

	overrides := &runner.JobOverrides{
		Args:      args,
		Env:       env,
		Resources: &runner.Resources{CPU: "2", Memory: "4Gi"},
		TaskCount: params.TaskCount,
	}

	req := runner.JobRequest{
		Name:      "syneHQ-analytics-job",
		JobID:     "",
		Command:   "analytics",
		Resources: runner.Resources{CPU: "1", Memory: "2Gi"},
		Type:      runner.JobTypeOneTime,
		Overrides: overrides,
	}

	result, err := client.RunJob(ctx, "/app/rover", req)
	if err != nil {
		return fmt.Errorf("failed to execute analytics job: %w", err)
	}

	fmt.Printf("Analytics job completed successfully: %s\n", result)
	return nil
}

// RunAnalyticsJobExample demonstrates how to invoke the analytics container job
// with parameters and environment variables, similar to build.sh test_container.
func RunAnalyticsJobExample() {
	ctx := context.Background()

	// Infisical secrets (injected as env vars in the container)
	infisicalSecrets := []models.Secret{
		{SecretKey: "DATABASE_URL", SecretValue: "postgresql://user:pass@localhost:5432/analytics"},
		{SecretKey: "API_KEY", SecretValue: "sk-1234567890abcdef"},
		{SecretKey: "REDIS_URL", SecretValue: "redis://localhost:6379"},
	}

	localRunner := runner.NewLocalRunner("synehq/analytics:latest", infisicalSecrets)

	// Example: Data export job (matches build.sh test_container)
	exportParams := AnalyticsParams{
		QueryType:    "export",
		Database:     "analytics",
		OutputFormat: "json",
		Parallelism:  2,
		ExecutionID:  "test-exec-123",
		UserID:       "test-user-456",
		TaskCount:    1,
		DatabasePath: "/app/analytics.db",
		APIKey:       "test-api-key",
		RedisURL:     "redis://localhost:6379",
		LogLevel:     "debug",
	}

	if err := executeAnalyticsJob(ctx, localRunner, exportParams); err != nil {
		log.Fatalf("Export job failed: %v", err)
	}

	// Example: Report generation job (with different parameters)
	reportParams := AnalyticsParams{
		QueryType:    "report",
		Database:     "analytics",
		OutputFormat: "json",
		Parallelism:  1,
		ExecutionID:  "test-exec-124",
		UserID:       "test-user-789",
		TaskCount:    1,
		DatabasePath: "/app/analytics.db",
		APIKey:       "test-api-key",
		RedisURL:     "redis://localhost:6379",
		LogLevel:     "debug",
	}

	if err := executeAnalyticsJob(ctx, localRunner, reportParams); err != nil {
		log.Fatalf("Report job failed: %v", err)
	}

	// Example: Cloud Run runner (commented out, see build.sh for local test)
	/*
		cloudSecrets := []models.Secret{
			{SecretKey: "DATABASE_URL", SecretValue: "postgresql://cloud-user:pass@cloud-host:5432/analytics"},
			{SecretKey: "API_KEY", SecretValue: "sk-cloud-1234567890abcdef"},
			{SecretKey: "REDIS_URL", SecretValue: "redis://cloud-redis:6379"},
		}
		cloudRunner := runner.NewCloudRunRunner("your-project", "us-central1", "synehq/analytics:latest", cloudSecrets)

		cloudParams := AnalyticsParams{
			QueryType:    "aggregation",
			Database:     "warehouse",
			OutputFormat: "parquet",
			Parallelism:  8,
			ExecutionID:  "exec-125",
			UserID:       "user-101",
			TaskCount:    4,
			DatabasePath: "/app/analytics.db",
			APIKey:       "cloud-api-key",
			RedisURL:     "redis://cloud-redis:6379",
			LogLevel:     "info",
		}

		if err := executeAnalyticsJob(ctx, cloudRunner, cloudParams); err != nil {
			log.Fatalf("Cloud analytics job failed: %v", err)
		}
	*/
}

func main() {
	RunAnalyticsJobExample()
}
