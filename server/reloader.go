package server

import (
	"context"
	"fmt"
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
		req := runner.JobRequest{
			JobID:          fmt.Sprintf("%s-%d", r.Name, time.Now().Unix()),
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
		err := s.sched.Schedule(r.Name, spec, func(c context.Context) error {
			start := time.Now().Unix()
			result, err := s.runner.RunJob(c, r.Prefix, req)
			end := time.Now().Unix()
			s.recordExecution(c, req, req.JobID, result, err, start, end)
			return err
		})
		if err != nil {
			log.Printf("failed to restore schedule for %s: %v", r.Name, err)
		}
		log.Printf("restored schedule for %s", r.Name)

		// small delay to avoid thundering herd on boot
		time.Sleep(50 * time.Millisecond)

		nextRun, ok := s.sched.NextRun(r.Name)
		if !ok {
			log.Printf("no next run found for %s", r.Name)
			continue
		}
		log.Printf("next run for %s: %s", r.Name, nextRun.Format(time.RFC3339))
	}

	log.Printf("restored %d schedules", len(records))
}
