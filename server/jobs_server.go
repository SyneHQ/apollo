package server

import (
	"context"
	"fmt"
	"log"
	"time"

	cfg "github.com/SyneHQ/apollo"
	"github.com/SyneHQ/apollo/proto"
	"github.com/SyneHQ/apollo/runner"
	"github.com/SyneHQ/apollo/scheduler"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

type JobsServer struct {
	proto.UnimplementedJobsServiceServer
	runner    runner.Runner
	cfg       *cfg.Config
	sched     *scheduler.Scheduler
	store     *scheduler.Store
	authority JobAuthority
}

func NewJobsServer(r runner.Runner, c *cfg.Config) (*JobsServer, error) {
	var sch *scheduler.Scheduler
	var st *scheduler.Store
	if (c.JobsProvider == "local" || c.JobsProvider == "hakopod") && c.Store.Driver != "" && c.Store.Path != "" {
		open := scheduler.OpenStore
		if c.Environment == "production" && c.Store.Driver == "postgres" {
			open = scheduler.OpenRuntimeStore
		}
		st, err := open(c.Store.Driver, c.Store.Path)
		if err != nil {
			return nil, fmt.Errorf("scheduler storage initialization failed")
		}
		sch = scheduler.New(cron.DefaultLogger)
		return &JobsServer{runner: r, cfg: c, sched: sch, store: st}, nil
	}
	return &JobsServer{runner: r, cfg: c, sched: sch, store: st}, nil
}

func (s *JobsServer) RunJob(ctx context.Context, req *proto.RunJobRequest) (*proto.RunJobResponse, error) {
	if req.Resources == nil {
		req.Resources = &proto.Resources{}
	}
	user, _ := ctx.Value(userContextKey{}).(string)
	r := runner.JobRequest{
		AuthorizedUser: user,
		Name:           req.GetName(),
		Command:        req.GetCommand(),
		Image:          req.GetImage(),
		Prefix:         req.GetPrefix(),
		ArgsJSONBase64: req.GetArgsBase64(),
		Resources:      runner.Resources{CPU: req.GetResources().Cpu, Memory: req.GetResources().Memory},
		Type:           mapJobType(req.GetType()),
		ScheduleSpec:   req.GetSchedule(),
	}
	// default resources if not provided
	if r.Resources.CPU == "" && r.Resources.Memory == "" {
		res := s.cfg.GetResourcesFor(r.Command)
		r.Resources.CPU = res.CPU
		r.Resources.Memory = res.Memory
	}

	if r.Type == runner.JobTypeRepeatable && s.sched != nil && r.ScheduleSpec != "" {
		name := r.Name
		err := s.sched.ScheduleSaved(name, r.ScheduleSpec, func(c context.Context) error {
			r := r
			start := time.Now().Unix()

			r.JobID = name + "-" + uuid.NewString()

			log.Printf("Running job %s", r.JobID)
			result, err := s.runAuthorized(c, r.Prefix, r)
			end := time.Now().Unix()
			log.Printf("Job %s completed", r.JobID)
			s.recordExecution(c, r, r.JobID, result, err, start, end)
			return err
		}, func() error {
			if s.store == nil {
				return fmt.Errorf("scheduler storage is unavailable")
			}
			return s.store.Upsert(ctx, scheduler.JobRecord{
				Name:           r.Name,
				AuthorizedUser: r.AuthorizedUser,
				Image:          r.Image,
				Command:        r.Command,
				Cpu:            r.Resources.CPU,
				Memory:         r.Resources.Memory,
				Prefix:         r.Prefix,
				CronSpec:       r.ScheduleSpec,
				ArgsBase64:     r.ArgsJSONBase64,
			})
		})

		nextRun, ok := s.sched.NextRun(r.Name)
		if !ok {
			log.Printf("no next run found for %s", r.Name)
		} else {
			log.Printf("next run for %s: %s", r.Name, nextRun.Format(time.RFC3339))
		}

		if err != nil {
			return nil, err
		}

		return &proto.RunJobResponse{Id: name, Logs: "scheduled"}, nil
	}

	start := time.Now().Unix()

	r.JobID = r.Name + "-" + uuid.NewString()

	log.Printf("Running job %s", r.JobID)

	s.recordExecution(ctx, r, r.JobID, "", nil, start, 0)

	result, err := s.runAuthorized(ctx, r.Prefix, r)

	end := time.Now().Unix()

	s.recordExecution(ctx, r, r.JobID, result, err, start, end)

	if err != nil {
		return nil, err
	}
	return &proto.RunJobResponse{Id: r.JobID, Logs: result}, nil
}

func (s *JobsServer) recordExecution(ctx context.Context, r runner.JobRequest, id string, result string, runErr error, start, optionalEnd int64) {
	end := optionalEnd
	isRunning := optionalEnd == 0
	if !isRunning {
		// A canceled caller or scheduler must not erase the final execution status.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
	}
	if s.store == nil {
		log.Println("No store found")
		return
	}

	// Determine status: "running" if job hasn't finished, otherwise "error" or "success"
	var status string
	if isRunning {
		status = "running"
	} else {
		status = map[bool]string{true: "error", false: "success"}[runErr != nil]
	}

	rec := scheduler.ExecutionRecord{
		ID:         id,
		Name:       r.Name,
		Command:    r.Command,
		ArgsBase64: r.ArgsJSONBase64,
		Cpu:        r.Resources.CPU,
		Image:      r.Image,
		Memory:     r.Resources.Memory,
		Prefix:     r.Prefix,
		Status:     status,
		Error: func() string {
			if runErr != nil {
				return runErr.Error()
			}
			return ""
		}(),
		Result:     result,
		StartedAt:  start,
		FinishedAt: end,
	}
	err := s.store.AddExecution(ctx, rec)
	if err != nil {
		log.Println("Error adding execution to store", err)
	}
}

func (s *JobsServer) DeleteJob(ctx context.Context, req *proto.DeleteJobRequest) (*proto.DeleteJobResponse, error) {
	if s.sched != nil {
		s.sched.Delete(req.GetName())
	}
	if s.store != nil {
		_ = s.store.Delete(ctx, req.GetName())
	}
	owner, _ := ctx.Value(teamContextKey{}).(string)
	if err := s.runner.DeleteJob(runner.WithOwnerScope(ctx, owner), req.GetName()); err != nil {
		return nil, err
	}
	return &proto.DeleteJobResponse{}, nil
}

func (s *JobsServer) UpdateSchedule(ctx context.Context, req *proto.UpdateScheduleRequest) (*proto.UpdateScheduleResponse, error) {
	name := req.GetName()
	spec := req.GetSchedule()
	if s.sched != nil {
		if spec == "" {
			s.sched.Delete(name)
			return &proto.UpdateScheduleResponse{}, nil
		}
		// server-managed reschedule requires the original command; advise client to call RunJob again
		return &proto.UpdateScheduleResponse{}, fmt.Errorf("reschedule requires rerun with RunJob in local provider")
	}
	// Cloud provider path
	if err := s.runner.UpdateSchedule(ctx, name, spec); err != nil {
		return nil, err
	}
	return &proto.UpdateScheduleResponse{}, nil
}

func (s *JobsServer) ListSchedules(ctx context.Context, req *proto.ListSchedulesRequest) (*proto.ListSchedulesResponse, error) {
	if s.store == nil {
		return &proto.ListSchedulesResponse{Items: []*proto.ScheduleItem{}}, nil
	}
	recs, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*proto.ScheduleItem, 0, len(recs))
	for _, r := range recs {
		team, _ := ctx.Value(teamContextKey{}).(string)
		if s.authority == nil || team == "" {
			continue
		}
		owner, err := s.authority.Owner(ctx, r.Name, false)
		if err != nil || owner != team {
			continue
		}
		out = append(out, &proto.ScheduleItem{
			Name:       r.Name,
			Command:    r.Command,
			ArgsBase64: r.ArgsBase64,
			Cron:       r.CronSpec,
			Resources:  &proto.Resources{Cpu: r.Cpu, Memory: r.Memory},
			Prefix:     r.Prefix,
			Image:      r.Image,
		})
	}
	return &proto.ListSchedulesResponse{Items: out}, nil
}

func mapJobType(t proto.JobType) runner.JobType {
	switch t {
	case proto.JobType_JOB_TYPE_REPEATABLE:
		return runner.JobTypeRepeatable
	default:
		return runner.JobTypeOneTime
	}
}

// Start enables restored schedules after authorization and startup checks succeed.
func (s *JobsServer) Start() {
	if s.sched != nil {
		s.sched.Start()
	}
}

// Close cancels scheduled executions and bounds shutdown by the caller's deadline.
func (s *JobsServer) Close(ctx context.Context) error {
	if s.sched != nil {
		select {
		case <-s.sched.Stop().Done():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.store != nil {
		return s.store.Close()
	}
	return nil
}
