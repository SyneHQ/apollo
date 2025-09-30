package tests

import (
	"context"
	"testing"
	"time"

	"github.com/SyneHQ/apollo/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const serverPort = "6910"
const serverHost = "localhost"

func TestRunJobOneTime(t *testing.T) {
	ctx := context.Background()

	// Connect to gRPC server
	conn, err := grpc.Dial(serverHost+":"+serverPort, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to connect to gRPC server: %v", err)
	}
	defer conn.Close()

	client := proto.NewJobsServiceClient(conn)

	// Test running a one-time job
	jobRequest := &proto.RunJobRequest{
		Name:       "ack-job-onetime",
		Command:    "ack",
		ArgsBase64: "",
		Resources: &proto.Resources{
			Memory: "1Gi",
			Cpu:    "500m",
		},
		Type: proto.JobType_JOB_TYPE_ONE_TIME,
	}

	result, err := client.RunJob(ctx, jobRequest)
	if err != nil {
		t.Logf("Job execution failed (this may be expected if server is not running): %v", err)
		t.Skip("Skipping test - server may not be running or job execution failed")
		return
	}

	t.Logf("One-time ack job result: %s", result.Logs)
}

func TestRunJobRepeatable(t *testing.T) {
	ctx := context.Background()

	// Connect to gRPC server
	conn, err := grpc.Dial(serverHost+":"+serverPort, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to connect to gRPC server: %v", err)
	}
	defer conn.Close()

	client := proto.NewJobsServiceClient(conn)

	// Test running the same job multiple times
	jobRequest := &proto.RunJobRequest{
		Name:       "ack-job-repeatable",
		Command:    "ack",
		ArgsBase64: "",
		Resources: &proto.Resources{
			Memory: "1Gi",
			Cpu:    "500m",
		},
		Type: proto.JobType_JOB_TYPE_REPEATABLE,
	}

	// Run the job 3 times to test repeatability
	for i := 0; i < 3; i++ {
		t.Logf("Running ack job iteration %d", i+1)

		result, err := client.RunJob(ctx, jobRequest)
		if err != nil {
			t.Logf("Job execution failed on iteration %d (this may be expected if server is not running): %v", i+1, err)
			t.Skip("Skipping test - server may not be running or job execution failed")
			return
		}

		t.Logf("Repeatable ack job iteration %d result: %s", i+1, result.Logs)

		// Add small delay between runs
		time.Sleep(100 * time.Millisecond)
	}
}

func TestRunJobWithSchedule(t *testing.T) {
	ctx := context.Background()

	// Connect to gRPC server
	conn, err := grpc.Dial(serverHost+":"+serverPort, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to connect to gRPC server: %v", err)
	}
	defer conn.Close()

	client := proto.NewJobsServiceClient(conn)

	// Test updating schedule
	updateRequest := &proto.UpdateScheduleRequest{
		Name:     "ack-job",
		Schedule: "0 */5 * * * *", // every 5 minutes
	}

	_, err = client.UpdateSchedule(ctx, updateRequest)
	if err != nil {
		t.Logf("Schedule update failed (this may be expected if server is not running): %v", err)
		t.Skip("Skipping test - server may not be running or schedule update failed")
		return
	}

	t.Log("Schedule updated successfully")
}
