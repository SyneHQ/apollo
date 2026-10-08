package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestPostgresRuntimeStoreRequiresReviewedSchema(t *testing.T) {
	dsn := os.Getenv("SECURITY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	suffix := fmt.Sprint(time.Now().UnixNano())
	schema := "apollo_runtime_" + suffix
	role := "apollo_runtime_" + suffix
	if _, err = db.Exec(`CREATE SCHEMA ` + schema + `; CREATE ROLE ` + role + ` LOGIN PASSWORD 'disposable-runtime-only'`); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DROP SCHEMA ` + schema + ` CASCADE; DROP ROLE ` + role)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	ownerDSN := u.String()
	migrated, err := OpenStore("postgres", ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	migrated.Close()
	if _, err = db.Exec(`GRANT USAGE ON SCHEMA ` + schema + ` TO ` + role + `; GRANT SELECT,INSERT,UPDATE,DELETE ON ` + schema + `.apollo_jobs,` + schema + `.apollo_executions TO ` + role + `; GRANT SELECT ON ` + schema + `.apollo_schema_receipts TO ` + role); err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, "disposable-runtime-only")
	runtime, err := OpenRuntimeStore("postgres", u.String())
	if err != nil {
		t.Fatalf("DML runtime cannot open reviewed schema: %v", err)
	}
	ctx := context.Background()
	if err = runtime.AddExecution(ctx, ExecutionRecord{ID: "fixture", Name: "fixture", Command: "ack", Status: "success"}); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.GetExecution(ctx, "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.db.Exec(`CREATE TABLE ` + schema + `.forbidden(id int)`); err == nil {
		t.Fatal("runtime unexpectedly has DDL permission")
	}
	if _, err = runtime.db.Exec(`UPDATE apollo_schema_receipts SET version='forbidden'`); err == nil {
		t.Fatal("runtime can modify schema receipt")
	}
	runtime.Close()
	for _, change := range []string{
		`UPDATE ` + schema + `.apollo_schema_receipts SET version='wrong'`,
		`DELETE FROM ` + schema + `.apollo_schema_receipts`,
		`INSERT INTO ` + schema + `.apollo_schema_receipts VALUES(1,'` + SchemaVersion + `'); ALTER TABLE ` + schema + `.apollo_jobs DROP COLUMN command`,
	} {
		if _, err = db.Exec(change); err != nil {
			t.Fatal(err)
		}
		store, err := OpenRuntimeStore("postgres", u.String())
		if err == nil {
			store.Close()
			t.Fatal("runtime accepted incompatible schema")
		}
	}
}
