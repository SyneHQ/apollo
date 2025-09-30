package runner

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	batch "cloud.google.com/go/batch/apiv1"
	batchpb "cloud.google.com/go/batch/apiv1/batchpb"
	scheduler "cloud.google.com/go/scheduler/apiv1"
	spb "cloud.google.com/go/scheduler/apiv1/schedulerpb"
	"github.com/infisical/go-sdk/packages/models"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

type BatchRunner struct {
	ProjectID string
	Region    string
	Image     string
	// Optional additional client options (e.g., custom credentials)
	ClientOptions []option.ClientOption
	// Optional service account email for Cloud Scheduler HTTP OAuth
	ServiceAccountEmail string
	Secrets             []models.Secret
	// Storage configuration
	PersistentDiskName string
	PersistentDiskSize int64
	PersistentDiskType string
}

func NewBatchRunner(projectID, region, image string, secrets []models.Secret) *BatchRunner {
	return &BatchRunner{
		ProjectID:          projectID,
		Region:             region,
		Image:              image,
		Secrets:            secrets,
		PersistentDiskSize: 64,            // Default 64GB
		PersistentDiskType: "pd-balanced", // Default balanced disk
	}
}

func (b *BatchRunner) jobName(id string) string {
	return fmt.Sprintf("projects/%s/locations/%s/jobs/%s", b.ProjectID, b.Region, id)
}

func (b *BatchRunner) parent() string {
	return fmt.Sprintf("projects/%s/locations/%s", b.ProjectID, b.Region)
}

func (b *BatchRunner) RunJob(ctx context.Context, cmd string, req JobRequest) (string, error) {
	client, err := batch.NewClient(ctx, b.ClientOptions...)
	if err != nil {
		return "", err
	}
	defer client.Close()

	// Build environment variables as a map[string]string
	envMap := make(map[string]string)
	// Add Infisical secrets
	for _, secret := range b.Secrets {
		envMap[secret.SecretKey] = secret.SecretValue
	}
	// Add client-provided environment variables
	if req.Overrides != nil && len(req.Overrides.Env) > 0 {
		for _, envVar := range req.Overrides.Env {
			envMap[envVar.Name] = envVar.Value
		}
	}

	// Define the runnable (script or container)
	var containerArgs []string
	if req.Overrides != nil && len(req.Overrides.Args) > 0 {
		containerArgs = req.Overrides.Args
	}
	runnable := &batchpb.Runnable{
		Executable: &batchpb.Runnable_Container_{
			Container: &batchpb.Runnable_Container{
				ImageUri: b.Image,
				Commands: []string{cmd},
				Options:  strings.Join(containerArgs, " "),
			},
		},
		Environment: &batchpb.Environment{
			Variables: envMap,
		},
	}

	// Configure storage volumes if persistent disk is specified
	var volumes []*batchpb.Volume
	var attachedDisks []*batchpb.AllocationPolicy_AttachedDisk

	if b.PersistentDiskName != "" {
		volume := &batchpb.Volume{
			MountPath: fmt.Sprintf("/mnt/disks/%s", b.PersistentDiskName),
			Source: &batchpb.Volume_DeviceName{
				DeviceName: b.PersistentDiskName,
			},
			MountOptions: []string{"rw", "async"},
		}
		volumes = append(volumes, volume)

		disk := &batchpb.AllocationPolicy_Disk{
			Type:   b.PersistentDiskType,
			SizeGb: b.PersistentDiskSize,
		}

		attachedDisk := &batchpb.AllocationPolicy_AttachedDisk{
			Attached: &batchpb.AllocationPolicy_AttachedDisk_NewDisk{
				NewDisk: disk,
			},
			DeviceName: b.PersistentDiskName,
		}
		attachedDisks = append(attachedDisks, attachedDisk)
	}

	// Define task specification
	taskSpec := &batchpb.TaskSpec{
		ComputeResource: &batchpb.ComputeResource{
			CpuMilli:  parseCPU(req.Resources.CPU),
			MemoryMib: parseMemory(req.Resources.Memory),
		},
		MaxRunDuration: &durationpb.Duration{Seconds: 24 * 60 * 60}, // 24 hours
		MaxRetryCount:  3,
		Runnables:      []*batchpb.Runnable{runnable},
		Volumes:        volumes,
	}

	// Task count from overrides or default to 1
	taskCount := int64(1)
	if req.Overrides != nil && req.Overrides.TaskCount > 0 {
		taskCount = int64(req.Overrides.TaskCount)
	}

	taskGroup := &batchpb.TaskGroup{
		TaskCount: taskCount,
		TaskSpec:  taskSpec,
	}

	// Define allocation policy
	instancePolicy := &batchpb.AllocationPolicy_InstancePolicy{
		MachineType: "n1-standard-1", // Default machine type
		Disks:       attachedDisks,
	}

	allocationPolicy := &batchpb.AllocationPolicy{
		Instances: []*batchpb.AllocationPolicy_InstancePolicyOrTemplate{{
			PolicyTemplate: &batchpb.AllocationPolicy_InstancePolicyOrTemplate_Policy{
				Policy: instancePolicy,
			},
		}},
	}

	// Create and submit the job
	job := &batchpb.Job{
		TaskGroups:       []*batchpb.TaskGroup{taskGroup},
		AllocationPolicy: allocationPolicy,
		Labels:           map[string]string{"env": "production", "type": "batch"},
		LogsPolicy: &batchpb.LogsPolicy{
			Destination: batchpb.LogsPolicy_CLOUD_LOGGING,
		},
	}

	createReq := &batchpb.CreateJobRequest{
		Parent: b.parent(),
		JobId:  req.Name,
		Job:    job,
	}

	op, err := client.CreateJob(ctx, createReq)
	if err != nil {
		return "", err
	}

	return op.GetName(), nil
}

func (b *BatchRunner) DeleteJob(ctx context.Context, name string) error {
	client, err := batch.NewClient(ctx, b.ClientOptions...)
	if err != nil {
		return err
	}
	defer client.Close()

	op, err := client.DeleteJob(ctx, &batchpb.DeleteJobRequest{
		Name: b.jobName(name),
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil
		}
		return err
	}
	err = op.Wait(ctx)
	return err
}

func (b *BatchRunner) UpdateSchedule(ctx context.Context, name string, spec string) error {
	sched, err := scheduler.NewCloudSchedulerClient(ctx, b.ClientOptions...)
	if err != nil {
		return err
	}
	defer sched.Close()

	parent := fmt.Sprintf("projects/%s/locations/%s", b.ProjectID, b.Region)
	jobName := fmt.Sprintf("%s/jobs/%s", parent, name)

	// Convert cron spec to 5-field format
	cronSpec := toFiveFieldCron(spec)

	// Target: HTTP call to Batch API
	url := fmt.Sprintf("https://batch.googleapis.com/v1/projects/%s/locations/%s/jobs", b.ProjectID, b.Region)

	// Create the job configuration as JSON body
	jobConfig := fmt.Sprintf(`{
        "job_id": "%s",
        "job": {
            "task_groups": [
                {
                    "task_count": 1,
                    "task_spec": {
                        "runnables": [
                            {
                                "container": {
                                    "image_uri": "%s"
                                }
                            }
                        ]
                    }
                }
            ],
            "allocation_policy": {
                "instances": [
                    {
                        "policy": {
                            "machine_type": "n1-standard-1"
                        }
                    }
                ]
            }
        }
    }`, name, b.Image)

	httpTarget := &spb.HttpTarget{
		HttpMethod: spb.HttpMethod_POST,
		Uri:        url,
		AuthorizationHeader: &spb.HttpTarget_OidcToken{
			OidcToken: &spb.OidcToken{
				ServiceAccountEmail: b.ServiceAccountEmail,
			},
		},
		Body: []byte(jobConfig),
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
	}

	desired := &spb.Job{
		Name:        jobName,
		Schedule:    cronSpec,
		TimeZone:    "UTC",
		Target:      &spb.Job_HttpTarget{HttpTarget: httpTarget},
		Description: "Run Batch Job",
	}

	// Try get existing
	if _, err := sched.GetJob(ctx, &spb.GetJobRequest{Name: jobName}); err != nil {
		if status.Code(err) != codes.NotFound {
			return err
		}
		// Create new
		_, err := sched.CreateJob(ctx, &spb.CreateJobRequest{Parent: parent, Job: desired})
		return err
	}
	// Update existing
	_, err = sched.UpdateJob(ctx, &spb.UpdateJobRequest{Job: desired})
	return err
}

// Helper functions
func parseCPU(cpu string) int64 {
	// Convert CPU string (e.g., "1000m" or "1") to milliseconds
	if strings.HasSuffix(cpu, "m") {
		cpu = strings.TrimSuffix(cpu, "m")
		if val, err := strconv.ParseInt(cpu, 10, 64); err == nil {
			return val
		}
	}
	if val, err := strconv.ParseInt(cpu, 10, 64); err == nil {
		return val * 1000 // Convert cores to millicores
	}
	return 1000 // Default to 1 core
}

func parseMemory(memory string) int64 {
	// Convert memory string (e.g., "1Gi", "1024Mi") to MiB
	memory = strings.ToUpper(memory)
	if strings.HasSuffix(memory, "GI") {
		memory = strings.TrimSuffix(memory, "GI")
		if val, err := strconv.ParseInt(memory, 10, 64); err == nil {
			return val * 1024 // Convert GiB to MiB
		}
	}
	if strings.HasSuffix(memory, "MI") {
		memory = strings.TrimSuffix(memory, "MI")
		if val, err := strconv.ParseInt(memory, 10, 64); err == nil {
			return val
		}
	}
	return 512 // Default to 512 MiB
}

func toFiveFieldCron(in string) string {
	fields := strings.Fields(in)
	if len(fields) == 6 {
		return strings.Join(fields[1:], " ")
	}
	return in
}
