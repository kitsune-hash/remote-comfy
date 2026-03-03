package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/kitsune-hash/remote-comfy/gateway/models"
)

type Store struct {
	db *sql.DB
}

func New(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS jobs (
			id TEXT PRIMARY KEY,
			workflow TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			started_at DATETIME,
			completed_at DATETIME,
			output_urls TEXT,
			error TEXT DEFAULT '',
			worker_id TEXT DEFAULT '',
			duration_ms INTEGER DEFAULT 0
		)
	`)
	return err
}

func (s *Store) CreateJob(id string, workflow interface{}) error {
	wfJSON, err := json.Marshal(workflow)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		"INSERT INTO jobs (id, workflow, status) VALUES (?, ?, ?)",
		id, string(wfJSON), models.StatusPending,
	)
	return err
}

func (s *Store) GetJob(id string) (*models.Job, error) {
	row := s.db.QueryRow(
		"SELECT id, workflow, status, created_at, started_at, completed_at, output_urls, error, worker_id, duration_ms FROM jobs WHERE id = ?",
		id,
	)

	var j models.Job
	var outputURLsJSON sql.NullString
	var startedAt, completedAt sql.NullTime

	err := row.Scan(&j.ID, &j.Workflow, &j.Status, &j.CreatedAt, &startedAt, &completedAt, &outputURLsJSON, &j.Error, &j.WorkerID, &j.DurationMs)
	if err != nil {
		return nil, err
	}

	if startedAt.Valid {
		j.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		j.CompletedAt = &completedAt.Time
	}
	if outputURLsJSON.Valid && outputURLsJSON.String != "" {
		json.Unmarshal([]byte(outputURLsJSON.String), &j.OutputURLs)
	}

	return &j, nil
}

func (s *Store) UpdateJobStatus(id string, status models.JobStatus) error {
	now := time.Now()
	switch status {
	case models.StatusRunning:
		_, err := s.db.Exec("UPDATE jobs SET status = ?, started_at = ? WHERE id = ?", status, now, id)
		return err
	case models.StatusCompleted, models.StatusFailed, models.StatusTimeout:
		_, err := s.db.Exec("UPDATE jobs SET status = ?, completed_at = ? WHERE id = ?", status, now, id)
		return err
	default:
		_, err := s.db.Exec("UPDATE jobs SET status = ? WHERE id = ?", status, id)
		return err
	}
}

func (s *Store) SetJobWorker(id, workerID string) error {
	_, err := s.db.Exec("UPDATE jobs SET worker_id = ? WHERE id = ?", workerID, id)
	return err
}

func (s *Store) CompleteJob(id string, outputURLs []string, durationMs int64) error {
	urlsJSON, _ := json.Marshal(outputURLs)
	now := time.Now()
	_, err := s.db.Exec(
		"UPDATE jobs SET status = ?, completed_at = ?, output_urls = ?, duration_ms = ? WHERE id = ?",
		models.StatusCompleted, now, string(urlsJSON), durationMs, id,
	)
	return err
}

func (s *Store) FailJob(id, errMsg string) error {
	now := time.Now()
	_, err := s.db.Exec(
		"UPDATE jobs SET status = ?, completed_at = ?, error = ? WHERE id = ?",
		models.StatusFailed, now, errMsg, id,
	)
	return err
}

func (s *Store) GetTimedOutJobs(timeout time.Duration) ([]string, error) {
	cutoff := time.Now().UTC().Add(-timeout)
	rows, err := s.db.Query(
		"SELECT id FROM jobs WHERE status IN ('pending', 'running') AND created_at < ?",
		cutoff,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
