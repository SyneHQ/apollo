package scheduler

import (
	"context"
	"database/sql"
	"errors"

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

type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS jobs (
        name TEXT PRIMARY KEY,
        command TEXT NOT NULL,
        args_base64 TEXT,
        cron_spec TEXT NOT NULL,
        cpu TEXT,
        memory TEXT
    )`)
	return err
}

func (s *Store) Upsert(ctx context.Context, r JobRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO jobs (name, command, args_base64, cron_spec, cpu, memory)
        VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT(name) DO UPDATE SET command=excluded.command, args_base64=excluded.args_base64, cron_spec=excluded.cron_spec, cpu=excluded.cpu, memory=excluded.memory`,
		r.Name, r.Command, r.ArgsBase64, r.CronSpec, r.Cpu, r.Memory)
	return err
}

func (s *Store) Delete(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE name = ?`, name)
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
	rows, err := s.db.QueryContext(ctx, `SELECT name, command, args_base64, cron_spec, cpu, memory FROM jobs`)
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
