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
)

type JobsServer struct {
	proto.UnimplementedJobsServiceServer
	runner runner.Runner
	cfg    *cfg.Config
	sched  *scheduler.Scheduler
	store  *scheduler.Store
}

func NewJobsServer(r runner.Runner, c *cfg.Config) *JobsServer {
	var sch *scheduler.Scheduler
	var st *scheduler.Store
	if c.JobsProvider == "local" && c.Store.Driver != "" && c.Store.Path != "" {
		sch = scheduler.New()
		// best-effort open local sqlite at ./jobs.db
		s, err := scheduler.OpenStore(c.Store.Driver, c.Store.Path)
		if err == nil {
			st = s
		}
	}
	return &JobsServer{runner: r, cfg: c, sched: sch, store: st}
}

func (s *JobsServer) RunJob(ctx context.Context, req *proto.RunJobRequest) (*proto.RunJobResponse, error) {
	r := runner.JobRequest{
		Name:           req.GetName(),
		Command:        req.GetCommand(),
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
		err := s.sched.Schedule(name, r.ScheduleSpec, func(c context.Context) {
			start := time.Now().Unix()
			if r.JobID == "" {
				r.JobID = fmt.Sprintf("job-%s-%d", req.Name, time.Now().Unix())
			}
			log.Printf("Running job %s with cmd: %s and command: %s", r.JobID, s.cfg.Jobs.Cmd, r.Command)
			result, runErr := s.runner.RunJob(c, s.cfg.Jobs.Cmd, r)
			end := time.Now().Unix()
			s.recordExecution(c, r, r.JobID, result, runErr, start, end)
		})
		if err != nil {
			return nil, err
		}
		if s.store != nil {
			_ = s.store.Upsert(ctx, scheduler.JobRecord{
				Name:    r.Name,
				Command: r.Command,
				Cpu:     r.Resources.CPU,
				Memory:  r.Resources.Memory,
			})
		}
		return &proto.RunJobResponse{Id: name, Logs: "scheduled"}, nil
	}
	start := time.Now().Unix()

	if r.JobID == "" {
		r.JobID = fmt.Sprintf("job-%s-%d", req.Name, time.Now().Unix())
	}

	log.Printf("Running job %s with cmd: %s and command: %s", r.JobID, s.cfg.Jobs.Cmd, r.Command)

	result, err := s.runner.RunJob(ctx, s.cfg.Jobs.Cmd, r)
	end := time.Now().Unix()
	s.recordExecution(ctx, r, r.JobID, result, err, start, end)
	if err != nil {
		return nil, err
	}
	return &proto.RunJobResponse{Id: r.JobID, Logs: result}, nil
}

func (s *JobsServer) recordExecution(ctx context.Context, r runner.JobRequest, id string, result string, runErr error, start, end int64) {
	if s.store == nil {
		log.Println("No store found")
		return
	}
	rec := scheduler.ExecutionRecord{
		ID:         id,
		Name:       r.Name,
		Command:    r.Command,
		ArgsBase64: r.ArgsJSONBase64,
		Cpu:        r.Resources.CPU,
		Memory:     r.Resources.Memory,
		Status:     map[bool]string{true: "error", false: "success"}[runErr != nil],
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
	if err := s.runner.DeleteJob(ctx, req.GetName()); err != nil {
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
		out = append(out, &proto.ScheduleItem{
			Name:       r.Name,
			Command:    r.Command,
			ArgsBase64: r.ArgsBase64,
			Cron:       r.CronSpec,
			Resources:  &proto.Resources{Cpu: r.Cpu, Memory: r.Memory},
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
