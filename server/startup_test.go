package server

import (
	"context"
	config "github.com/SyneHQ/apollo"
	"github.com/SyneHQ/apollo/runner"
	"path/filepath"
	"testing"
	"time"
)

func TestUnavailableStoreRejectsServer(t *testing.T) {
	_, err := NewJobsServer(&executionProbe{}, &config.Config{JobsProvider: "local", Store: config.StoreConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "missing", "jobs.db")}})
	if err == nil {
		t.Fatal("storage failure must reject server startup")
	}
}
func TestStorageRestoresBeforeStart(t *testing.T) {
	s, err := NewJobsServer(&executionProbe{}, &config.Config{JobsProvider: "local", Store: config.StoreConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "jobs.db")}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer s.Close(ctx)
	if err := s.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.sched.Schedule("test", "@every 1s", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.sched.NextRun("test"); ok {
		t.Fatal("scheduler started before startup checks")
	}
	s.Start()
}

func TestFinalExecutionStatusSurvivesCallerCancellation(t *testing.T) {
	s, err := NewJobsServer(&executionProbe{}, &config.Config{JobsProvider: "local", Store: config.StoreConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "jobs.db")}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.recordExecution(ctx, runner.JobRequest{Name: "job"}, "execution", "", context.Canceled, 1, 2)
	rec, err := s.store.GetExecution(context.Background(), "execution")
	if err != nil || rec.Status != "error" || rec.FinishedAt != 2 {
		t.Fatalf("terminal status missing: %+v %v", rec, err)
	}
}
