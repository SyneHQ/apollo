package server

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/SyneHQ/apollo/runner"
	"github.com/google/uuid"
)

// Reload schedules from store at startup
func (s *JobsServer) Reload(ctx context.Context) error {
	if s.sched == nil || s.store == nil {
		return nil
	}
	records, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("cannot load stored schedules")
	}

	log.Printf("restoring %d schedules", len(records))

	for _, r := range records {
		req := runner.JobRequest{
			AuthorizedUser: r.AuthorizedUser,

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
			req := req
			req.JobID = req.Name + "-" + uuid.NewString()
			start := time.Now().Unix()
			result, err := s.runAuthorized(c, r.Prefix, req)
			end := time.Now().Unix()
			s.recordExecution(c, req, req.JobID, result, err, start, end)
			return err
		})
		if err != nil {
			return fmt.Errorf("cannot restore stored schedule")
		}
		log.Printf("restored schedule for %s", r.Name)

		nextRun, ok := s.sched.NextRun(r.Name)
		if !ok {
			log.Printf("no next run found for %s", r.Name)
			continue
		}
		log.Printf("next run for %s: %s", r.Name, nextRun.Format(time.RFC3339))
	}

	log.Printf("restored %d schedules", len(records))
	return nil
}
