package scheduler

import (
	"context"
	"path/filepath"
	"testing"
)

func TestScheduleRetainsAuthorizingUser(t *testing.T) {
	s, err := OpenStore("sqlite", filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := JobRecord{Name: "flowr-pipeline-p", Command: "code", CronSpec: "0 * * * *", AuthorizedUser: "owner-a"}
	if err = s.Upsert(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(context.Background())
	if err != nil || len(rows) != 1 || rows[0].AuthorizedUser != "owner-a" {
		t.Fatalf("lost schedule owner: %v %+v", err, rows)
	}
}
