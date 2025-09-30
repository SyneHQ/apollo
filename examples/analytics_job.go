package analytics_job

import (
	"context"
	"fmt"
	"log"

	"github.com/SyneHQ/apollo/runner"
	"github.com/infisical/go-sdk/packages/models"
)

// AnalyticsParams represents the parameters for analytics job execution
type AnalyticsParams struct {
	QueryType    string
	Database     string
	OutputFormat string
	Parallelism  int
	ExecutionID  string
	UserID       string
	TaskCount    int32
}

// executeAnalyticsJob demonstrates how to use reusable jobs with runtime overrides
func executeAnalyticsJob(ctx context.Context, client runner.Runner, params AnalyticsParams) error {
	// Create job overrides with runtime parameters
	overrides := &runner.JobOverrides{
		Args: []string{
			"--query-type", params.QueryType,
			"--database", params.Database,
			"--output-format", params.OutputFormat,
			"--parallelism", fmt.Sprintf("%d", params.Parallelism),
		},
		Env: []runner.EnvVar{
			{Name: "EXECUTION_ID", Value: params.ExecutionID},
			{Name: "USER_ID", Value: params.UserID},
		},
		Resources: &runner.Resources{
			CPU:    "2",
			Memory: "4Gi",
		},
		TaskCount: params.TaskCount,
	}

	// Create job request with overrides
	req := runner.JobRequest{
		Name:    "syneHQ-analytics-job", // Reusable job name
		JobID:   "",                     // Will be auto-generated
		Command: "analytics",            // Base command
		Resources: runner.Resources{ // Default resources (can be overridden)
			CPU:    "1",
			Memory: "2Gi",
		},
		Type:      runner.JobTypeOneTime,
		Overrides: overrides,
	}

	// Execute the job
	result, err := client.RunJob(ctx, "/app/rover", req)
	if err != nil {
		return fmt.Errorf("failed to execute analytics job: %w", err)
	}

	fmt.Printf("Analytics job completed successfully: %s\n", result)
	return nil
}

// Example usage scenarios
func RunAnalyticsJobExample() {
	ctx := context.Background()

	// Example 1: Local runner with Infisical secrets
	// Infisical secrets will be automatically injected as environment variables
	infisicalSecrets := []models.Secret{
		{SecretKey: "DATABASE_URL", SecretValue: "postgresql://user:pass@localhost:5432/analytics"},
		{SecretKey: "API_KEY", SecretValue: "sk-1234567890abcdef"},
		{SecretKey: "REDIS_URL", SecretValue: "redis://localhost:6379"},
	}
	localRunner := runner.NewLocalRunner("synehq/analytics:latest", infisicalSecrets)

	// Scenario 1: Data export job
	// This will have Infisical secrets (DATABASE_URL, API_KEY, REDIS_URL) automatically injected
	// Plus client-provided environment variables (EXECUTION_ID, USER_ID)
	exportParams := AnalyticsParams{
		QueryType:    "export",
		Database:     "production",
		OutputFormat: "csv",
		Parallelism:  4,
		ExecutionID:  "exec-123",
		UserID:       "user-456",
		TaskCount:    2,
	}

	if err := executeAnalyticsJob(ctx, localRunner, exportParams); err != nil {
		log.Fatalf("Export job failed: %v", err)
	}

	// Scenario 2: Report generation job
	// This will also have Infisical secrets automatically injected
	// Client can override Infisical secrets by providing the same environment variable name
	reportParams := AnalyticsParams{
		QueryType:    "report",
		Database:     "analytics",
		OutputFormat: "json",
		Parallelism:  2,
		ExecutionID:  "exec-124",
		UserID:       "user-789",
		TaskCount:    1,
	}

	if err := executeAnalyticsJob(ctx, localRunner, reportParams); err != nil {
		log.Fatalf("Report job failed: %v", err)
	}

	// Example 2: Cloud Run runner (commented out as it requires GCP setup)
	/*
		// Cloud Run also supports Infisical secrets injection
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
		}

		if err := executeAnalyticsJob(ctx, cloudRunner, cloudParams); err != nil {
			log.Fatalf("Cloud analytics job failed: %v", err)
		}
	*/
}
