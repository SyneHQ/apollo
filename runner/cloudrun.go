package runner

import (
	"context"
	"fmt"
	"strings"

	run "cloud.google.com/go/run/apiv2"
	rpb "cloud.google.com/go/run/apiv2/runpb"
	scheduler "cloud.google.com/go/scheduler/apiv1"
	spb "cloud.google.com/go/scheduler/apiv1/schedulerpb"
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
}

func NewCloudRunRunner(projectID, region, image string) *CloudRunRunner {
	return &CloudRunRunner{ProjectID: projectID, Region: region, Image: image}
}

func (c *CloudRunRunner) jobName(id string) string {
	return fmt.Sprintf("projects/%s/locations/%s/jobs/%s", c.ProjectID, c.Region, id)
}

func (c *CloudRunRunner) parent() string {
	return fmt.Sprintf("projects/%s/locations/%s", c.ProjectID, c.Region)
}

func (c *CloudRunRunner) ensureJob(ctx context.Context, client *run.JobsClient, req JobRequest) error {
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
							Image:   c.Image,
							Command: []string{"/app/rover"},
							Args:    buildArgs(req),
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

func buildArgs(req JobRequest) []string {
	args := []string{req.Command}
	if req.ArgsJSONBase64 != "" {
		args = append(args, req.ArgsJSONBase64)
	}
	return args
}

func (c *CloudRunRunner) RunJob(ctx context.Context, req JobRequest) (string, error) {
	client, err := run.NewJobsClient(ctx, c.ClientOptions...)
	if err != nil {
		return "", err
	}
	defer client.Close()
	if err := c.ensureJob(ctx, client, req); err != nil {
		return "", err
	}
	op, err := client.RunJob(ctx, &rpb.RunJobRequest{Name: c.jobName(req.Name)})
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
