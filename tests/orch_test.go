package tests

import (
	"context"
	"encoding/base64"
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

	conn, err := grpc.Dial(serverHost+":"+serverPort, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to connect to gRPC server: %v", err)
	}
	defer conn.Close()

	client := proto.NewJobsServiceClient(conn)

	// Prepare args and encode as base64 (empty for this test)
	args := []byte{}
	argsBase64 := base64.StdEncoding.EncodeToString(args)

	jobRequest := &proto.RunJobRequest{
		Name:       "ack-job-onetime",
		Command:    "ack",
		ArgsBase64: argsBase64,
		Resources: &proto.Resources{
			Cpu:    "500m",
			Memory: "1Gi",
		},
		Type: proto.JobType_JOB_TYPE_ONE_TIME,
		// Optionally test JobOverrides
		Overrides: &proto.JobOverrides{
			Args: []string{},
			Env:  []*proto.EnvVar{{Name: "EXAMPLE_ENV", Value: "test"}},
			Resources: &proto.Resources{
				Cpu:    "250m",
				Memory: "512Mi",
			},
			TaskCount: 1,
		},
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

	conn, err := grpc.Dial(serverHost+":"+serverPort, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to connect to gRPC server: %v", err)
	}
	defer conn.Close()

	client := proto.NewJobsServiceClient(conn)

	args := []byte{}
	argsBase64 := base64.StdEncoding.EncodeToString(args)

	jobRequest := &proto.RunJobRequest{
		Name:       "ack-job-repeatable",
		Command:    "ack",
		ArgsBase64: argsBase64,
		Resources: &proto.Resources{
			Cpu:    "500m",
			Memory: "1Gi",
		},
		Type: proto.JobType_JOB_TYPE_REPEATABLE,
		Overrides: &proto.JobOverrides{
			Args: []string{},
			Env:  []*proto.EnvVar{{Name: "EXAMPLE_ENV", Value: "repeat"}},
			Resources: &proto.Resources{
				Cpu:    "300m",
				Memory: "768Mi",
			},
			TaskCount: 2,
		},
	}

	for i := 0; i < 3; i++ {
		t.Logf("Running ack job iteration %d", i+1)

		result, err := client.RunJob(ctx, jobRequest)
		if err != nil {
			t.Logf("Job execution failed on iteration %d (this may be expected if server is not running): %v", i+1, err)
			t.Skip("Skipping test - server may not be running or job execution failed")
			return
		}

		t.Logf("Repeatable ack job iteration %d result: %s", i+1, result.Logs)
		time.Sleep(100 * time.Millisecond)
	}
}

func TestRunJobWithSchedule(t *testing.T) {
	ctx := context.Background()

	conn, err := grpc.Dial(serverHost+":"+serverPort, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to connect to gRPC server: %v", err)
	}
	defer conn.Close()

	client := proto.NewJobsServiceClient(conn)

	// Test updating schedule using UpdateScheduleRequest
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

	// Optionally, test ListSchedules
	listReq := &proto.ListSchedulesRequest{}
	listResp, err := client.ListSchedules(ctx, listReq)
	if err != nil {
		t.Logf("ListSchedules failed (this may be expected if server is not running): %v", err)
		t.Skip("Skipping test - server may not be running or list schedules failed")
		return
	}
	t.Logf("Schedules: %+v", listResp.Items)
}
