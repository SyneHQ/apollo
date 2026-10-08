package scheduler

import (
	"context"
	"errors"
	"github.com/robfig/cron/v3"
	"sync/atomic"
	"testing"
	"time"
)

func TestFailedReplacementPreservesSchedule(t *testing.T) {
	s := New(cron.DefaultLogger)
	defer s.Stop()
	fn := func(context.Context) error { return nil }
	if err := s.Schedule("job", "@every 1h", fn); err != nil {
		t.Fatal(err)
	}
	original := s.entries["job"]
	called := false
	if err := s.ScheduleSaved("job", "invalid", fn, func() error { called = true; return nil }); err == nil || called {
		t.Fatal("invalid schedule reached persistence")
	}
	if err := s.ScheduleSaved("job", "@every 2h", fn, func() error { return errors.New("store unavailable") }); err == nil {
		t.Fatal("store failure was ignored")
	}
	if s.entries["job"] != original || s.cron.Entry(original).ID == 0 {
		t.Fatal("old schedule was lost")
	}
}

func TestNoOverlapAcrossScheduleReplacement(t *testing.T) {
	s := New(cron.DefaultLogger)
	defer s.Stop()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	if err := s.Schedule("job", "@every 1h", func(context.Context) error { close(started); <-release; return nil }); err != nil {
		t.Fatal(err)
	}
	original := s.cron.Entry(s.entries["job"]).Job
	go func() { defer close(done); original.Run() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("job did not start")
	}
	var calls atomic.Int32
	if err := s.Schedule("job", "@every 1h", func(context.Context) error { calls.Add(1); return nil }); err != nil {
		t.Fatal(err)
	}
	replacement := s.cron.Entry(s.entries["job"]).Job
	replacement.Run()
	if calls.Load() != 0 {
		t.Fatal("replacement overlapped running execution")
	}
	close(release)
	<-done
	replacement.Run()
	if calls.Load() != 1 {
		t.Fatal("replacement did not run after prior execution")
	}
	s.Stop()
	replacement.Run()
	if calls.Load() != 1 {
		t.Fatal("job ran after shutdown")
	}
}
