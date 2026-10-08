package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLegacySQLiteMigrationPreservesRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE apollo_executions(id TEXT PRIMARY KEY,name TEXT NOT NULL,command TEXT NOT NULL,args_base64 TEXT,cpu TEXT,memory TEXT,status TEXT,error TEXT,result TEXT,started_at BIGINT,finished_at BIGINT); INSERT INTO apollo_executions VALUES('old','job','cmd','','','','success','','',1,2)`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM apollo_executions WHERE id='old'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("row lost: %v count=%d", err, count)
	}
	if _, err := s.GetExecution(context.Background(), "old"); err != nil {
		t.Fatal(err)
	}
	if s.db.Stats().MaxOpenConnections != 1 {
		t.Fatal("SQLite must use one configured connection")
	}
}
func TestPostgresStoreMigrationAndDuplicates(t *testing.T) {
	dsn := os.Getenv("SECURITY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := fmt.Sprintf("apollo_store_%d", time.Now().UnixNano())
	if _, err = db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	scoped := dsn + sep + "search_path=" + schema
	s, err := OpenStore("postgres", scoped)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	e := ExecutionRecord{ID: "one", Name: "job", Command: "cmd", Status: "running"}
	if err = s.AddExecution(ctx, e); err != nil {
		t.Fatal(err)
	}
	e.Status = "success"
	if err = s.AddExecution(ctx, e); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err = db.Exec(`DROP INDEX ` + schema + `.uq_apollo_executions_id; ALTER TABLE ` + schema + `.apollo_executions DROP CONSTRAINT apollo_executions_pkey; INSERT INTO ` + schema + `.apollo_executions SELECT * FROM ` + schema + `.apollo_executions`); err != nil {
		t.Fatal(err)
	}
	if broken, err := OpenStore("postgres", scoped); err == nil {
		broken.Close()
		t.Fatal("duplicate history must block migration")
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM ` + schema + `.apollo_executions`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("history was deleted: %v count=%d", err, count)
	}
}
