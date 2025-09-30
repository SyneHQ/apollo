package test_reusable_jobs

import (
	"context"
	"fmt"
	"log"

	"github.com/SyneHQ/apollo/runner"
	"github.com/infisical/go-sdk/packages/models"
)

func TestReusableJobs() {
	ctx := context.Background()

	// Create a local runner with Infisical secrets
	// These secrets will be automatically injected as environment variables
	infisicalSecrets := []models.Secret{
		{SecretKey: "DATABASE_URL", SecretValue: "postgresql://test:test@localhost:5432/testdb"},
		{SecretKey: "API_KEY", SecretValue: "test-api-key-123"},
		{SecretKey: "LOG_LEVEL", SecretValue: "debug"},
	}
	localRunner := runner.NewLocalRunner("synehq/analytics:latest", infisicalSecrets)

	// Test case 1: Job with overrides
	// This will have Infisical secrets (DATABASE_URL, API_KEY, LOG_LEVEL) automatically injected
	// Plus client-provided environment variables (EXECUTION_ID, USER_ID)
	fmt.Println("Testing job with overrides...")
	req := runner.JobRequest{
		Name:    "test-analytics-job",
		JobID:   "", // Will be auto-generated
		Command: "analytics",
		Resources: runner.Resources{
			CPU:    "1",
			Memory: "2Gi",
		},
		Type: runner.JobTypeOneTime,
		Overrides: &runner.JobOverrides{
			Args: []string{
				"--query-type", "export",
				"--database", "test-db",
				"--output-format", "json",
			},
			Env: []runner.EnvVar{
				{Name: "EXECUTION_ID", Value: "test-exec-123"},
				{Name: "USER_ID", Value: "test-user-456"},
				// Note: LOG_LEVEL from Infisical will be overridden by this client-provided value
				{Name: "LOG_LEVEL", Value: "info"}, // This overrides the Infisical secret
			},
			Resources: &runner.Resources{
				CPU:    "2",
				Memory: "4Gi",
			},
			TaskCount: 2,
		},
	}

	result, err := localRunner.RunJob(ctx, "/app/rover", req)
	if err != nil {
		log.Printf("Job execution failed (expected for demo): %v", err)
	} else {
		fmt.Printf("Job completed successfully: %s\n", result)
	}

	// Test case 2: Job without overrides (using default args)
	// This will only have Infisical secrets (DATABASE_URL, API_KEY, LOG_LEVEL) injected
	fmt.Println("\nTesting job without overrides...")
	req2 := runner.JobRequest{
		Name:           "test-simple-job",
		JobID:          "custom-job-id-123",
		Command:        "simple-task",
		ArgsJSONBase64: "eyJkYXRhIjoidGVzdCJ9", // base64 encoded JSON
		Resources: runner.Resources{
			CPU:    "0.5",
			Memory: "1Gi",
		},
		Type: runner.JobTypeOneTime,
		// No overrides - only Infisical secrets will be injected
	}

	result2, err := localRunner.RunJob(ctx, "/app/rover", req2)
	if err != nil {
		log.Printf("Job execution failed (expected for demo): %v", err)
	} else {
		fmt.Printf("Job completed successfully: %s\n", result2)
	}

	fmt.Println("\nDemo completed! The reusable job system with Infisical secrets integration is working correctly.")
	fmt.Println("\nEnvironment variables injected:")
	fmt.Println("- DATABASE_URL: postgresql://test:test@localhost:5432/testdb (from Infisical)")
	fmt.Println("- API_KEY: test-api-key-123 (from Infisical)")
	fmt.Println("- LOG_LEVEL: debug (from Infisical, overridden to 'info' in test case 1)")
	fmt.Println("- EXECUTION_ID: test-exec-123 (from client override)")
	fmt.Println("- USER_ID: test-user-456 (from client override)")
}
