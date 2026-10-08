package server

import (
	"context"
	config "github.com/SyneHQ/apollo"
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
