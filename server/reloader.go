package server

import (
	"context"
	"log"
	"time"

	"github.com/SyneHQ/apollo/runner"
)

// Reload schedules from store at startup
func (s *JobsServer) Reload(ctx context.Context) {
	if s.sched == nil || s.store == nil {
		return
	}
	records, err := s.store.List(ctx)
	if err != nil {
		log.Printf("scheduler reload failed: %v", err)
		return
	}

	log.Printf("restoring %d schedules", len(records))

	for _, r := range records {
		if r.Prefix == "" {
			r.Prefix = s.cfg.Jobs.Cmd
		}
		req := runner.JobRequest{
			Name:           r.Name,
			Command:        r.Command,
			ArgsJSONBase64: r.ArgsBase64,
			Image:          r.Image,
			Prefix:         r.Prefix,
			Resources:      runner.Resources{CPU: r.Cpu, Memory: r.Memory},
			Type:           runner.JobTypeRepeatable,
			ScheduleSpec:   r.CronSpec,
		}
		spec := r.CronSpec
		err := s.sched.Schedule(r.Name, spec, func(c context.Context) {
			_, _ = s.runner.RunJob(c, r.Prefix, req)
		})
		if err != nil {
			log.Printf("failed to restore schedule for %s: %v", r.Name, err)
		}
		log.Printf("restored schedule for %s", r.Name)
		// small delay to avoid thundering herd on boot
		time.Sleep(50 * time.Millisecond)
	}

	log.Printf("restored %d schedules", len(records))
}
