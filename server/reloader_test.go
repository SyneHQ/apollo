package server

import (
	"context"
	config "github.com/SyneHQ/apollo"
	"github.com/SyneHQ/apollo/runner"
	"github.com/SyneHQ/apollo/scheduler"
	"path/filepath"
	"testing"
	"time"
)

type replayProbe struct{ ids chan string }

func (p *replayProbe) RunJob(_ context.Context, _ string, r runner.JobRequest) (string, error) {
	p.ids <- r.JobID
	return "done", nil
}
func (p *replayProbe) DeleteJob(context.Context, string) error              { return nil }
func (p *replayProbe) UpdateSchedule(context.Context, string, string) error { return nil }

func TestRestoredScheduleUsesDistinctExecutionIDs(t *testing.T) {
	probe := &replayProbe{ids: make(chan string, 8)}
	s, err := NewJobsServer(probe, &config.Config{JobsProvider: "local", Store: config.StoreConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "jobs.db")}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer s.Close(ctx)
	s.SetAuthority(fakeAuthority{member: true})
	record := scheduler.JobRecord{Name: "backup-a", AuthorizedUser: "owner", Command: "handleBackupJob", Prefix: "/app/rover", Image: roverImage(), ArgsBase64: encoded(map[string]string{"backupScheduleId": "a"}), CronSpec: "@every 1s"}
	if err := s.store.Upsert(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	s.Start()
	var ids []string
	for len(ids) < 2 {
		select {
		case id := <-probe.ids:
			ids = append(ids, id)
		case <-ctx.Done():
			t.Fatal("scheduled runs did not finish")
		}
	}
	<-s.sched.Stop().Done()
	if ids[0] == ids[1] || ids[0] == "" {
		t.Fatal("restored executions reused an ID")
	}
	records, err := s.store.ListExecutions(ctx, "backup-a", 10)
	if err != nil || len(records) != 2 {
		t.Fatalf("expected two execution records: count=%d err=%v", len(records), err)
	}
}
