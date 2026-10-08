package main

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestSchedulerMigrationDoesNotLoadRuntimeProviders(t *testing.T) {
	dsn := os.Getenv("SECURITY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := fmt.Sprintf("apollo_cli_%d", time.Now().UnixNano())
	if _, err = db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	t.Setenv("APOLLO_MIGRATION_DATABASE_URL", u.String())
	t.Setenv("APOLLO_SERVICE_TOKEN", "")
	t.Setenv("METADATA_DATABASE_URL", "")
	t.Setenv("USE_INFISICAL", "true")
	t.Setenv("INFISICAL_API_URL", "not-a-runtime-endpoint")
	for i := 0; i < 2; i++ {
		if err := migrateStore(); err != nil {
			t.Fatalf("explicit migration required runtime configuration: %v", err)
		}
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM ` + schema + `.apollo_schema_receipts WHERE id=1`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration receipt missing: %v", err)
	}
}
func TestSchedulerMigrationRequiresExplicitOwnerConnection(t *testing.T) {
	t.Setenv("APOLLO_MIGRATION_DATABASE_URL", "")
	t.Setenv("STORE_PATH", "must-not-fall-back-to-runtime")
	if migrateStore() == nil {
		t.Fatal("migration accepted a missing owner connection")
	}
}
