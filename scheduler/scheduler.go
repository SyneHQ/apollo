package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	cron "github.com/robfig/cron/v3"
)

// JobFunc is the function type executed by the scheduler.
//
// Implementations should honor context cancellation and deadlines.
type JobFunc func(ctx context.Context) error

// Scheduler wraps robfig/cron with named jobs, proper lifecycle control,
// and support for both 5-field and 6-field cron specs.
//
// Supported specs:
//   - "min hour dom month dow"      (e.g. "0 0 * * *")
//   - "sec min hour dom month dow"  (e.g. "0 0 0 * * *")
//   - descriptors like "@every 5m", "@hourly", "@midnight", etc.
type Scheduler struct {
	mu      sync.RWMutex
	cron    *cron.Cron
	entries map[string]cron.EntryID

	// baseCtx is used as the parent for all job contexts.
	baseCtx context.Context
	cancel  context.CancelFunc
}

// New creates a Scheduler with a robust cron parser and sane defaults.
//
// It uses a parser that accepts an optional seconds field and descriptors,
// which matches the recommended configuration in robfig/cron v3 docs.
func New(logger cron.Logger) *Scheduler {
	// Base context for all jobs; can be cancelled via Stop().
	baseCtx, cancel := context.WithCancel(context.Background())

	parser := cron.NewParser(
		cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
	)

	c := cron.New(
		cron.WithParser(parser),
		cron.WithLogger(logger),
		// Chain can be extended with custom wrappers (metrics, tracing, panic recovery, etc.).
		cron.WithChain(
			cron.Recover(logger),
		),
	)

	return &Scheduler{
		cron:    c,
		entries: make(map[string]cron.EntryID),
		baseCtx: baseCtx,
		cancel:  cancel,
	}
}

// Start begins the scheduler loop in its own goroutine.
//
// Start is safe to call multiple times; subsequent calls are no-ops.
func (s *Scheduler) Start() {
	s.cron.Start()
}

// Stop stops accepting new job executions and waits for running jobs to finish.
//
// It cancels the base context so jobs that check ctx.Done() can stop early.
// The returned context is done when all jobs have completed.
func (s *Scheduler) Stop() context.Context {
	// First prevent future job executions.
	stopCtx := s.cron.Stop()

	// Cancel the base context so jobs can react to shutdown.
	s.cancel()

	// Optionally, you can combine both: the scheduler is stopped when *both*
	// cron has drained and the base context is cancelled. Here we just return
	// cron's stopCtx, which completes when jobs finish.
	return stopCtx
}

// Schedule registers or replaces a named job on the given cron spec.
//
// If a job with the same name already exists, it is removed and replaced.
// To treat duplicates as errors instead, change the logic to check and return
// an error when name already exists.
//
// The supplied function is wrapped so it receives a derived context that:
//   - is cancelled when the scheduler is stopped.
//   - can be used for deadlines and propagation.
func (s *Scheduler) Schedule(name string, spec string, fn JobFunc) error {
	if name == "" {
		return fmt.Errorf("scheduler: job name must not be empty")
	}
	if fn == nil {
		return fmt.Errorf("scheduler: JobFunc must not be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove existing entry (if any) so re-scheduling is idempotent.
	if id, ok := s.entries[name]; ok {
		s.cron.Remove(id)
		delete(s.entries, name)
	}

	// Wrap fn to inject context and basic logging.
	wrapped := func() {
		ctx, cancel := context.WithCancel(s.baseCtx)
		defer cancel()

		if err := fn(ctx); err != nil {
			// Logging delegated to cron.Logger; add structured info via fmt.
			log.Printf("scheduler: job %s failed: %s", name, err)
		}
	}

	id, err := s.cron.AddFunc(spec, wrapped)
	if err != nil {
		return fmt.Errorf("scheduler: add job %q failed: %w", name, err)
	}

	s.entries[name] = id
	return nil
}

// Delete removes a job by name. It is a no-op if the job does not exist.
func (s *Scheduler) Delete(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id, ok := s.entries[name]; ok {
		s.cron.Remove(id)
		delete(s.entries, name)
	}
}

// NextRun returns the next scheduled run time for a named job.
// If the job is not found or has no next run, the second return value is false.
func (s *Scheduler) NextRun(name string) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	id, ok := s.entries[name]
	if !ok {
		return time.Time{}, false
	}

	e := s.cron.Entry(id)
	if e.ID == 0 {
		return time.Time{}, false
	}
	return e.Next, !e.Next.IsZero()
}

// List returns a snapshot of all scheduled job names and their next run time.
func (s *Scheduler) List() map[string]time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]time.Time, len(s.entries))
	for name, id := range s.entries {
		e := s.cron.Entry(id)
		out[name] = e.Next
	}
	return out
}
