package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"time"

	_ "github.com/lib/pq" // PostgreSQL
	_ "modernc.org/sqlite"
)

type JobRecord struct {
	Name       string
	Command    string
	ArgsBase64 string
	CronSpec   string
	Cpu        string
	Memory     string
}

type ExecutionRecord struct {
	ID         string
	Name       string
	Command    string
	ArgsBase64 string
	Cpu        string
	Memory     string
	Status     string
	Error      string
	Result     string
	StartedAt  int64
	FinishedAt int64
}

type Store struct {
	db     *sql.DB
	driver string
}

func OpenStore(driver, path string) (*Store, error) {
	db, err := sql.Open(driver, path)
	if err != nil {
		return nil, err
	}
	if driver == "sqlite" {
		db.Exec(`PRAGMA foreign_keys = ON`)
	}
	if driver == "postgres" {
		db.SetConnMaxIdleTime(15 * time.Minute)
		db.SetMaxIdleConns(10)
		db.SetMaxOpenConns(100)
		db.SetConnMaxLifetime(1 * time.Hour)
	}
	if err := migrate(db); err != nil {
		return nil, err
	}
	return &Store{db: db, driver: driver}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS apollo_jobs (
        name TEXT PRIMARY KEY,
        command TEXT NOT NULL,
        args_base64 TEXT,
        cron_spec TEXT NOT NULL,
        cpu TEXT,
        memory TEXT
    )`)
	if err != nil {
		return err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS apollo_executions (
        id TEXT,
        name TEXT NOT NULL,
        command TEXT NOT NULL,
        args_base64 TEXT,
        cpu TEXT,
        memory TEXT,
        status TEXT,
        error TEXT,
        result TEXT,
        started_at INTEGER,
        finished_at INTEGER
    )`)
	if err != nil {
		return err
	}
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_apollo_executions_name_started ON apollo_executions(name, started_at)`)
	return err
}

type DBDriver string

const (
	SQLite     DBDriver = "sqlite"
	PostgreSQL DBDriver = "postgres"
)

func (s *Store) IsSQLite() bool {
	return DBDriver(s.driver) == SQLite
}

func (s *Store) IsPostgres() bool {
	return DBDriver(s.driver) == PostgreSQL
}

func (s *Store) Upsert(ctx context.Context, r JobRecord) error {
	// Use UPSERT syntax appropriate for each database
	query := `INSERT INTO apollo_jobs (name, command, args_base64, cron_spec, cpu, memory)
        VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT(name) DO UPDATE SET 
            command = EXCLUDED.command, 
            args_base64 = EXCLUDED.args_base64, 
            cron_spec = EXCLUDED.cron_spec, 
            cpu = EXCLUDED.cpu, 
            memory = EXCLUDED.memory`

	// For SQLite, use REPLACE or INSERT OR REPLACE for better performance
	if s.IsSQLite() {
		query = `INSERT OR REPLACE INTO apollo_jobs (name, command, args_base64, cron_spec, cpu, memory)
            VALUES (?, ?, ?, ?, ?, ?)`
	}
	if s.IsPostgres() {
		query = `INSERT INTO apollo_jobs (name, command, args_base64, cron_spec, cpu, memory)
            VALUES ($1, $2, $3, $4, $5, $6)
            ON CONFLICT(name) DO UPDATE SET 
                command = EXCLUDED.command, 
                args_base64 = EXCLUDED.args_base64, 
                cron_spec = EXCLUDED.cron_spec, 
                cpu = EXCLUDED.cpu, 
                memory = EXCLUDED.memory`
	}

	_, err := s.db.ExecContext(ctx, query, r.Name, r.Command, r.ArgsBase64, r.CronSpec, r.Cpu, r.Memory)
	return err
}

func (s *Store) Delete(ctx context.Context, name string) error {
	query := `DELETE FROM apollo_jobs WHERE name = ?`
	if s.IsPostgres() {
		query = `DELETE FROM apollo_jobs WHERE name = $1`
	}
	res, err := s.db.ExecContext(ctx, query, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("not found")
	}
	return nil
}

func (s *Store) List(ctx context.Context) ([]JobRecord, error) {
	// Add ORDER BY for consistent results and potential index usage
	rows, err := s.db.QueryContext(ctx, `SELECT name, command, args_base64, cron_spec, cpu, memory 
        FROM apollo_jobs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []JobRecord
	for rows.Next() {
		var r JobRecord
		if err := rows.Scan(&r.Name, &r.Command, &r.ArgsBase64, &r.CronSpec, &r.Cpu, &r.Memory); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) AddExecution(ctx context.Context, e ExecutionRecord) error {
	// Use prepared statement pattern for better performance
	query := `INSERT INTO apollo_executions 
        (id, name, command, args_base64, cpu, memory, status, error, result, started_at, finished_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if s.IsPostgres() {
		query = `INSERT INTO apollo_executions 
        (id, name, command, args_base64, cpu, memory, status, error, result, started_at, finished_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	}
	_, err := s.db.ExecContext(ctx, query,
		e.ID, e.Name, e.Command, e.ArgsBase64, e.Cpu, e.Memory, e.Status, e.Error, e.Result, e.StartedAt, e.FinishedAt,
	)
	return err
}
