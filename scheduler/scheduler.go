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
	c := cron.New(cron.WithSeconds())
	c.Start()
	return &Scheduler{cron: c, entries: map[string]cron.EntryID{}}
}

// Schedule uses standard cron syntax (with seconds): "* * * * * *"
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
