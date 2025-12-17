package scheduler

import (
	"context"
	"sync"

	cron "github.com/robfig/cron/v3"
)

type JobFunc func(context.Context)

type Scheduler struct {
	mu      sync.Mutex
	cron    *cron.Cron
	entries map[string]cron.EntryID
}

func New() *Scheduler {
	// Accept both:
	// - 5-field cron: "min hour dom month dow" (e.g. "0 0 * * *")
	// - 6-field cron: "sec min hour dom month dow" (e.g. "0 0 0 * * *")
	//
	// Using WithSeconds() would require exactly 6 fields and breaks standard cron specs.
	parser := cron.NewParser(
		cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
	)
	c := cron.New(cron.WithParser(parser))
	c.Start()
	return &Scheduler{cron: c, entries: map[string]cron.EntryID{}}
}

// Schedule supports standard 5-field cron ("min hour dom month dow") and 6-field cron with seconds.
func (s *Scheduler) Schedule(name string, spec string, fn JobFunc) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.entries[name]; ok {
		s.cron.Remove(id)
		delete(s.entries, name)
	}
	id, err := s.cron.AddFunc(spec, func() { fn(context.Background()) })
	if err != nil {
		return err
	}
	s.entries[name] = id
	return nil
}

func (s *Scheduler) Delete(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.entries[name]; ok {
		s.cron.Remove(id)
		delete(s.entries, name)
	}
}
