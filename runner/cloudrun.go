package runner

import (
	"context"
	"fmt"
	"strings"
	"time"

	run "cloud.google.com/go/run/apiv2"
	rpb "cloud.google.com/go/run/apiv2/runpb"
	scheduler "cloud.google.com/go/scheduler/apiv1"
	spb "cloud.google.com/go/scheduler/apiv1/schedulerpb"
	"github.com/infisical/go-sdk/packages/models"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

type CloudRunRunner struct {
	ProjectID string
	Region    string
	Image     string
	// Optional additional client options (e.g., custom credentials)
	ClientOptions []option.ClientOption
	// Optional service account email for Cloud Scheduler HTTP OAuth
	ServiceAccountEmail string
	Secrets             []models.Secret
}

func NewCloudRunRunner(projectID, region, image string, secrets []models.Secret) *CloudRunRunner {
	return &CloudRunRunner{ProjectID: projectID, Region: region, Image: image, Secrets: secrets}
}

func (c *CloudRunRunner) jobName(id string) string {
	return fmt.Sprintf("projects/%s/locations/%s/jobs/%s", c.ProjectID, c.Region, id)
}

func (c *CloudRunRunner) parent() string {
	return fmt.Sprintf("projects/%s/locations/%s", c.ProjectID, c.Region)
}

func (c *CloudRunRunner) ensureJob(ctx context.Context, client *run.JobsClient, cmd string, req JobRequest) error {
	name := c.jobName(req.Name)
	// Check if job exists
	if _, err := client.GetJob(ctx, &rpb.GetJobRequest{Name: name}); err != nil {
		if status.Code(err) != codes.NotFound {
			return err
		}

		// Create new job
		job := &rpb.Job{
			Name: name,
			Template: &rpb.ExecutionTemplate{
				Template: &rpb.TaskTemplate{
					Containers: []*rpb.Container{
						{
							Image: c.Image,
							Command: []string{
								cmd,
							},
							Resources: &rpb.ResourceRequirements{Limits: map[string]string{
								"cpu":    req.Resources.CPU,
								"memory": req.Resources.Memory,
							}},
						},
					},
					Retries: &rpb.TaskTemplate_MaxRetries{MaxRetries: 3},
					Timeout: &durationpb.Duration{Seconds: 24 * 60 * 60},
				},
			},
		}
		op, err := client.CreateJob(ctx, &rpb.CreateJobRequest{Parent: c.parent(), Job: job, JobId: req.Name})
		if err != nil {
			return err
		}
		if _, err := op.Wait(ctx); err != nil {
			return err
		}
		return nil
	}
	// TODO: Optionally update job if resources/command changed
	return nil
}

func (c *CloudRunRunner) RunJob(ctx context.Context, cmd string, req JobRequest) (string, error) {
	client, err := run.NewJobsClient(ctx, c.ClientOptions...)
	if err != nil {
		return "", err
	}
	defer client.Close()

	// Generate job ID if not provided
	jobID := req.JobID
	if jobID == "" {
		jobID = fmt.Sprintf("job-%s-%d", req.Name, time.Now().Unix())
	}

	if err := c.ensureJob(ctx, client, cmd, req); err != nil {
		return "", err
	}

	// Build run request with overrides
	runReq := &rpb.RunJobRequest{Name: c.jobName(req.Name)}

	// Always inject Infisical secrets, and apply client overrides if provided
	overrides := &rpb.RunJobRequest_Overrides{}
	containerOverride := &rpb.RunJobRequest_Overrides_ContainerOverride{}

	// Combine Infisical secrets and client-provided environment variables
	// Client overrides will take precedence over Infisical secrets
	allEnvVars := make([]*rpb.EnvVar, 0, len(c.Secrets))
	if req.Overrides != nil {
		allEnvVars = make([]*rpb.EnvVar, 0, len(c.Secrets)+len(req.Overrides.Env))
	}

	// First add Infisical secrets
	for _, secret := range c.Secrets {
		allEnvVars = append(allEnvVars, &rpb.EnvVar{
			Name: secret.SecretKey,
			Values: &rpb.EnvVar_Value{
				Value: secret.SecretValue,
			},
		})
	}

	// Then add client-provided environment variables (these will override Infisical secrets)
	if req.Overrides != nil && len(req.Overrides.Env) > 0 {
		for _, envVar := range req.Overrides.Env {
			allEnvVars = append(allEnvVars, &rpb.EnvVar{
				Name: envVar.Name,
				Values: &rpb.EnvVar_Value{
					Value: envVar.Value,
				},
			})
		}
	}

	// Set environment variables if we have any
	if len(allEnvVars) > 0 {
		containerOverride.Env = allEnvVars
	}

	// Apply client overrides if provided
	if req.Overrides != nil {
		// Override args
		if len(req.Overrides.Args) > 0 {
			containerOverride.Args = req.Overrides.Args
		}

		// Override task count
		if req.Overrides.TaskCount > 0 {
			overrides.TaskCount = req.Overrides.TaskCount
		}
	}

	// Set container overrides if we have any
	if len(containerOverride.Args) > 0 || len(containerOverride.Env) > 0 {
		overrides.ContainerOverrides = []*rpb.RunJobRequest_Overrides_ContainerOverride{containerOverride}
	}

	// Set overrides if we have any
	if len(overrides.ContainerOverrides) > 0 || overrides.TaskCount > 0 {
		runReq.Overrides = overrides
	}

	op, err := client.RunJob(ctx, runReq)
	if err != nil {
		return "", err
	}
	exec, err := op.Wait(ctx)
	if err != nil {
		return "", err
	}
	return exec.GetName(), nil
}

func (c *CloudRunRunner) DeleteJob(ctx context.Context, name string) error {
	client, err := run.NewJobsClient(ctx, c.ClientOptions...)
	if err != nil {
		return err
	}
	defer client.Close()
	op, err := client.DeleteJob(ctx, &rpb.DeleteJobRequest{Name: c.jobName(name)})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil
		}
		return err
	}
	_, err = op.Wait(ctx)
	return err
}

func (c *CloudRunRunner) UpdateSchedule(ctx context.Context, name string, spec string) error {
	sched, err := scheduler.NewCloudSchedulerClient(ctx, c.ClientOptions...)
	if err != nil {
		return err
	}
	defer sched.Close()

	parent := fmt.Sprintf("projects/%s/locations/%s", c.ProjectID, c.Region)
	jobName := fmt.Sprintf("%s/jobs/%s", parent, name)

	// GCP Cron supports 5-fields; map from 6-field by dropping seconds if present
	cronSpec := toFiveFieldCron(spec)

	// Target: HTTP call to Run Job API
	// POST https://run.googleapis.com/v2/projects/{project}/locations/{region}/jobs/{job}:run
	url := fmt.Sprintf("https://run.googleapis.com/v2/%s:run", jobName)
	httpTarget := &spb.HttpTarget{
		HttpMethod: spb.HttpMethod_POST,
		Uri:        url,
		// Use OIDC token with provided service account
		AuthorizationHeader: &spb.HttpTarget_OidcToken{OidcToken: &spb.OidcToken{ServiceAccountEmail: c.ServiceAccountEmail}},
	}

	desired := &spb.Job{
		Name:        jobName,
		Schedule:    cronSpec,
		TimeZone:    "UTC",
		Target:      &spb.Job_HttpTarget{HttpTarget: httpTarget},
		Description: "Run Cloud Run Job",
	}

	// Try get existing
	if _, err := sched.GetJob(ctx, &spb.GetJobRequest{Name: jobName}); err != nil {
		if status.Code(err) != codes.NotFound {
			return err
		}
		// create
		_, err := sched.CreateJob(ctx, &spb.CreateJobRequest{Parent: parent, Job: desired})
		return err
	}
	// update
	_, err = sched.UpdateJob(ctx, &spb.UpdateJobRequest{Job: desired})
	return err
}

func toFiveFieldCron(in string) string {
	fields := strings.Fields(in)
	if len(fields) == 6 {
		return strings.Join(fields[1:], " ")
	}
	return in
}
