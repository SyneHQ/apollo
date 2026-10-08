package scheduler

import (
	"context"
	"errors"
)

const SchemaVersion = "20261009-scheduler-v1"

func (s *Store) markSchemaReady(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS apollo_schema_receipts (id INTEGER PRIMARY KEY CHECK (id=1), version TEXT NOT NULL)`); err != nil {
		return err
	}
	query := `INSERT INTO apollo_schema_receipts(id,version) VALUES (1,?) ON CONFLICT(id) DO UPDATE SET version=excluded.version`
	if s.driver == PostgreSQL {
		query = `INSERT INTO apollo_schema_receipts(id,version) VALUES (1,$1) ON CONFLICT(id) DO UPDATE SET version=excluded.version`
	}
	_, err := s.db.ExecContext(ctx, query, SchemaVersion)
	return err
}
func (s *Store) validateSchema(ctx context.Context) error {
	var version string
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM apollo_schema_receipts WHERE id=1`).Scan(&version); err != nil {
		return err
	}
	if version != SchemaVersion {
		return errors.New("scheduler schema version does not match")
	}
	for _, query := range []string{
		`SELECT name,command,args_base64,cron_spec,cpu,memory,image,prefix,authorized_user,created_at,updated_at FROM apollo_jobs LIMIT 0`,
		`SELECT id,name,command,args_base64,cpu,memory,image,prefix,status,error,result,started_at,finished_at,created_at,updated_at FROM apollo_executions LIMIT 0`,
	} {
		rows, err := s.db.QueryContext(ctx, query)
		if err != nil {
			return err
		}
		rows.Close()
	}
	return nil
}
