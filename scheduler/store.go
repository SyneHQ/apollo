package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/lib/pq" // PostgreSQL
	_ "modernc.org/sqlite"
)

// JobRecord represents a scheduled job configuration
type JobRecord struct {
	Name       string
	Command    string
	Prefix     string
	ArgsBase64 string
	CronSpec   string
	Cpu        string
	Memory     string
	Image      string
}

// ExecutionRecord represents a job execution instance
type ExecutionRecord struct {
	ID         string
	Name       string
	Command    string
	ArgsBase64 string
	Prefix     string
	Cpu        string
	Memory     string
	Image      string
	Status     string
	Error      string
	Result     string
	StartedAt  int64
	FinishedAt int64
}

// DBDriver represents supported database drivers
type DBDriver string

const (
	SQLite     DBDriver = "sqlite"
	PostgreSQL DBDriver = "postgres"
)

// Store provides database operations for job scheduling
type Store struct {
	db     *sql.DB
	driver DBDriver
}

// OpenStore creates a new Store instance with the specified driver and connection string
func OpenStore(driver, path string) (*Store, error) {
	if driver == "" || path == "" {
		return nil, errors.New("driver and path cannot be empty")
	}

	db, err := sql.Open(driver, path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	store := &Store{
		db:     db,
		driver: DBDriver(driver),
	}

	if err := store.configure(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to configure database: %w", err)
	}

	if err := store.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return store, nil
}

// configure sets database-specific configuration
func (s *Store) configure() error {
	switch s.driver {
	case SQLite:
		if _, err := s.db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
			return fmt.Errorf("failed to enable foreign keys: %w", err)
		}
		if _, err := s.db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
			return fmt.Errorf("failed to set WAL mode: %w", err)
		}
	case PostgreSQL:
		s.db.SetConnMaxIdleTime(15 * time.Minute)
		s.db.SetMaxIdleConns(10)
		s.db.SetMaxOpenConns(99)
		s.db.SetConnMaxLifetime(1 * time.Hour)
	}
	return nil
}

// migrate performs database schema migrations
func (s *Store) migrate() error {
	// Create jobs table first
	if err := s.createJobsTable(); err != nil {
		return fmt.Errorf("failed to create jobs table: %w", err)
	}

	// Create executions table
	if err := s.createExecutionsTable(); err != nil {
		return fmt.Errorf("failed to create executions table: %w", err)
	}

	// Add missing columns (for backward compatibility)
	if err := s.addMissingColumns(); err != nil {
		return fmt.Errorf("failed to add missing columns: %w", err)
	}

	// Create indexes
	if err := s.createIndexes(); err != nil {
		return fmt.Errorf("failed to create indexes: %w", err)
	}

	return nil
}

func (s *Store) createJobsTable() error {
	query := `CREATE TABLE IF NOT EXISTS apollo_jobs (
        name TEXT PRIMARY KEY,
        command TEXT NOT NULL,
        args_base64 TEXT,
        cron_spec TEXT NOT NULL,
        cpu TEXT,
        memory TEXT,
        image TEXT,
        prefix TEXT,
        created_at INTEGER DEFAULT (strftime('%s', 'now')),
        updated_at INTEGER DEFAULT (strftime('%s', 'now'))
    )`

	if s.driver == PostgreSQL {
		query = `CREATE TABLE IF NOT EXISTS apollo_jobs (
            name TEXT PRIMARY KEY,
            command TEXT NOT NULL,
            args_base64 TEXT,
            cron_spec TEXT NOT NULL,
            cpu TEXT,
            memory TEXT,
            image TEXT,
            prefix TEXT,
            created_at BIGINT DEFAULT EXTRACT(EPOCH FROM NOW()),
            updated_at BIGINT DEFAULT EXTRACT(EPOCH FROM NOW())
        )`
	}

	_, err := s.db.Exec(query)
	return err
}

func (s *Store) createExecutionsTable() error {
	query := `CREATE TABLE IF NOT EXISTS apollo_executions (
        id TEXT PRIMARY KEY,
        name TEXT NOT NULL,
        command TEXT NOT NULL,
        args_base64 TEXT,
        cpu TEXT,
        memory TEXT,
        image TEXT,
        prefix TEXT,
        status TEXT NOT NULL DEFAULT 'pending',
        error TEXT,
        result TEXT,
        started_at BIGINT,
        finished_at BIGINT,
        created_at INTEGER DEFAULT (strftime('%s', 'now'))
    )`

	if s.driver == PostgreSQL {
		query = `CREATE TABLE IF NOT EXISTS apollo_executions (
            id TEXT PRIMARY KEY,
            name TEXT NOT NULL,
            command TEXT NOT NULL,
            args_base64 TEXT,
            cpu TEXT,
            memory TEXT,
            image TEXT,
            prefix TEXT,
            status TEXT NOT NULL DEFAULT 'pending',
            error TEXT,
            result TEXT,
            started_at BIGINT,
            finished_at BIGINT,
            created_at BIGINT DEFAULT EXTRACT(EPOCH FROM NOW())
        )`
	}

	_, err := s.db.Exec(query)
	return err
}

func (s *Store) addMissingColumns() error {
	columns := []struct {
		table  string
		column string
		def    string
	}{
		{"apollo_jobs", "prefix", "TEXT"},
		{"apollo_jobs", "image", "TEXT"},
		{"apollo_executions", "prefix", "TEXT"},
		{"apollo_executions", "image", "TEXT"},
	}

	for _, col := range columns {
		query := fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s %s", col.table, col.column, col.def)
		if _, err := s.db.Exec(query); err != nil {
			// Ignore "duplicate column" errors for databases that don't support IF NOT EXISTS
			if !s.isColumnExistsError(err) {
				return fmt.Errorf("failed to add column %s.%s: %w", col.table, col.column, err)
			}
		}
	}

	return nil
}

func (s *Store) createIndexes() error {
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_apollo_executions_name_started ON apollo_executions(name, started_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_apollo_executions_status ON apollo_executions(status)",
		"CREATE INDEX IF NOT EXISTS idx_apollo_jobs_updated ON apollo_jobs(updated_at DESC)",
	}

	for _, idx := range indexes {
		if _, err := s.db.Exec(idx); err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}
	}

	return nil
}

func (s *Store) isColumnExistsError(err error) bool {
	errStr := err.Error()
	return contains(errStr, "duplicate column") || contains(errStr, "already exists")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || (len(s) > len(substr) &&
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			indexOf(s, substr) >= 0)))
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// Close closes the database connection
func (s *Store) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// IsSQLite returns true if the store is using SQLite
func (s *Store) IsSQLite() bool {
	return s.driver == SQLite
}

// IsPostgres returns true if the store is using PostgreSQL
func (s *Store) IsPostgres() bool {
	return s.driver == PostgreSQL
}

// Upsert inserts or updates a job record
func (s *Store) Upsert(ctx context.Context, r JobRecord) error {
	if r.Name == "" {
		return errors.New("job name cannot be empty")
	}

	var query string
	var args []interface{}

	switch s.driver {
	case SQLite:
		query = `INSERT OR REPLACE INTO apollo_jobs 
            (name, command, args_base64, cron_spec, cpu, memory, image, prefix, updated_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, strftime('%s', 'now'))`
		args = []interface{}{r.Name, r.Command, r.ArgsBase64, r.CronSpec, r.Cpu, r.Memory, r.Image, r.Prefix}

	case PostgreSQL:
		query = `INSERT INTO apollo_jobs 
            (name, command, args_base64, cron_spec, cpu, memory, image, prefix, updated_at)
            VALUES ($1, $2, $3, $4, $5, $6, $7, $8, EXTRACT(EPOCH FROM NOW()))
            ON CONFLICT(name) DO UPDATE SET 
                command = EXCLUDED.command, 
                args_base64 = EXCLUDED.args_base64, 
                cron_spec = EXCLUDED.cron_spec, 
                cpu = EXCLUDED.cpu, 
                memory = EXCLUDED.memory,
                image = EXCLUDED.image,
                prefix = EXCLUDED.prefix,
                updated_at = EXTRACT(EPOCH FROM NOW())`
		args = []interface{}{r.Name, r.Command, r.ArgsBase64, r.CronSpec, r.Cpu, r.Memory, r.Image, r.Prefix}

	default:
		return fmt.Errorf("unsupported database driver: %s", s.driver)
	}

	_, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to upsert job %s: %w", r.Name, err)
	}

	return nil
}

// Delete removes a job record by name
func (s *Store) Delete(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("job name cannot be empty")
	}

	var query string
	if s.IsPostgres() {
		query = `DELETE FROM apollo_jobs WHERE name = $1`
	} else {
		query = `DELETE FROM apollo_jobs WHERE name = ?`
	}

	res, err := s.db.ExecContext(ctx, query, name)
	if err != nil {
		return fmt.Errorf("failed to delete job %s: %w", name, err)
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("job %s not found", name)
	}

	return nil
}

// List returns all job records
func (s *Store) List(ctx context.Context) ([]JobRecord, error) {
	query := `SELECT name, command, args_base64, cron_spec, cpu, memory, image, prefix 
        FROM apollo_jobs ORDER BY name`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query jobs: %w", err)
	}
	defer rows.Close()

	var jobs []JobRecord
	for rows.Next() {
		var r JobRecord
		err := rows.Scan(&r.Name, &r.Command, &r.ArgsBase64, &r.CronSpec,
			&r.Cpu, &r.Memory, &r.Image, &r.Prefix)
		if err != nil {
			return nil, fmt.Errorf("failed to scan job record: %w", err)
		}
		jobs = append(jobs, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating job records: %w", err)
	}

	return jobs, nil
}

// AddExecution inserts or updates an execution record
func (s *Store) AddExecution(ctx context.Context, e ExecutionRecord) error {
	if e.ID == "" {
		return errors.New("execution ID cannot be empty")
	}
	if e.Name == "" {
		return errors.New("execution name cannot be empty")
	}

	var query string
	var args []interface{}

	switch s.driver {
	case SQLite:
		query = `INSERT OR REPLACE INTO apollo_executions 
            (id, name, command, args_base64, cpu, memory, image, prefix, status, error, result, started_at, finished_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
		args = []interface{}{e.ID, e.Name, e.Command, e.ArgsBase64, e.Cpu, e.Memory,
			e.Image, e.Prefix, e.Status, e.Error, e.Result, e.StartedAt, e.FinishedAt}

	case PostgreSQL:
		query = `INSERT INTO apollo_executions 
            (id, name, command, args_base64, cpu, memory, image, prefix, status, error, result, started_at, finished_at)
            VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
            ON CONFLICT(id) DO UPDATE SET 
                status = EXCLUDED.status,
                error = EXCLUDED.error,
                result = EXCLUDED.result,
                finished_at = EXCLUDED.finished_at`
		args = []interface{}{e.ID, e.Name, e.Command, e.ArgsBase64, e.Cpu, e.Memory,
			e.Image, e.Prefix, e.Status, e.Error, e.Result, e.StartedAt, e.FinishedAt}

	default:
		return fmt.Errorf("unsupported database driver: %s", s.driver)
	}

	_, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to add execution %s: %w", e.ID, err)
	}

	return nil
}

// GetExecution retrieves an execution record by ID
func (s *Store) GetExecution(ctx context.Context, id string) (*ExecutionRecord, error) {
	if id == "" {
		return nil, errors.New("execution ID cannot be empty")
	}

	var query string
	if s.IsPostgres() {
		query = `SELECT id, name, command, args_base64, cpu, memory, image, prefix, status, error, result, started_at, finished_at
            FROM apollo_executions WHERE id = $1`
	} else {
		query = `SELECT id, name, command, args_base64, cpu, memory, image, prefix, status, error, result, started_at, finished_at
            FROM apollo_executions WHERE id = ?`
	}

	var e ExecutionRecord
	err := s.db.QueryRowContext(ctx, query, id).Scan(
		&e.ID, &e.Name, &e.Command, &e.ArgsBase64, &e.Cpu, &e.Memory,
		&e.Image, &e.Prefix, &e.Status, &e.Error, &e.Result, &e.StartedAt, &e.FinishedAt,
	)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("execution %s not found", id)
		}
		return nil, fmt.Errorf("failed to get execution %s: %w", id, err)
	}

	return &e, nil
}

// ListExecutions returns execution records for a job, ordered by start time
func (s *Store) ListExecutions(ctx context.Context, jobName string, limit int) ([]ExecutionRecord, error) {
	if jobName == "" {
		return nil, errors.New("job name cannot be empty")
	}
	if limit <= 0 {
		limit = 100 // Default limit
	}

	var query string
	if s.IsPostgres() {
		query = `SELECT id, name, command, args_base64, cpu, memory, image, prefix, status, error, result, started_at, finished_at
            FROM apollo_executions WHERE name = $1 ORDER BY started_at DESC LIMIT $2`
	} else {
		query = `SELECT id, name, command, args_base64, cpu, memory, image, prefix, status, error, result, started_at, finished_at
            FROM apollo_executions WHERE name = ? ORDER BY started_at DESC LIMIT ?`
	}

	rows, err := s.db.QueryContext(ctx, query, jobName, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query executions for job %s: %w", jobName, err)
	}
	defer rows.Close()

	var executions []ExecutionRecord
	for rows.Next() {
		var e ExecutionRecord
		err := rows.Scan(&e.ID, &e.Name, &e.Command, &e.ArgsBase64, &e.Cpu, &e.Memory,
			&e.Image, &e.Prefix, &e.Status, &e.Error, &e.Result, &e.StartedAt, &e.FinishedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan execution record: %w", err)
		}
		executions = append(executions, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating execution records: %w", err)
	}

	return executions, nil
}
