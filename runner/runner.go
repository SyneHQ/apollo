package runner

import "context"

type JobType string

const (
	JobTypeOneTime    JobType = "one_time"
	JobTypeRepeatable JobType = "repeatable"
)

type JobRequest struct {
	Name           string
	Command        string
	ArgsJSONBase64 string
	Resources      Resources
	Type           JobType
	ScheduleSpec   string // cron spec if repeatable
}

type Resources struct {
	CPU    string
	Memory string
}

type Runner interface {
	RunJob(ctx context.Context, req JobRequest) (string, error)
	DeleteJob(ctx context.Context, name string) error
	UpdateSchedule(ctx context.Context, name string, spec string) error
}
