package runner

import "context"

type JobType string

const (
	JobTypeOneTime    JobType = "one_time"
	JobTypeRepeatable JobType = "repeatable"
)

type JobRequest struct {
	Name           string
	JobID          string // Optional: if not provided, will be auto-generated
	Command        string
	ArgsJSONBase64 string
	Resources      Resources
	Type           JobType
	ScheduleSpec   string        // cron spec if repeatable
	Overrides      *JobOverrides // Optional runtime overrides
}

type JobOverrides struct {
	Args      []string   // Override container args
	Env       []EnvVar   // Override environment variables
	Resources *Resources // Override resource limits
	TaskCount int32      // Override task count for parallel execution
}

type EnvVar struct {
	Name  string
	Value string
}

type Resources struct {
	CPU    string
	Memory string
}

type Runner interface {
	RunJob(ctx context.Context, prefix string, req JobRequest) (string, error)
	DeleteJob(ctx context.Context, name string) error
	UpdateSchedule(ctx context.Context, name string, spec string) error
}
