package models

import "time"

type JobStatus string

const (
	StatusPending   JobStatus = "pending"
	StatusRunning   JobStatus = "running"
	StatusCompleted JobStatus = "completed"
	StatusFailed    JobStatus = "failed"
	StatusTimeout   JobStatus = "timeout"
)

type Job struct {
	ID          string    `json:"id"`
	Workflow    string    `json:"workflow"`
	Status      JobStatus `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	OutputURLs  []string  `json:"output_urls,omitempty"`
	Error       string    `json:"error,omitempty"`
	WorkerID    string    `json:"worker_id,omitempty"`
	DurationMs  int64     `json:"duration_ms,omitempty"`
}

type ExecuteRequest struct {
	Workflow interface{} `json:"workflow" binding:"required"`
}

type ExecuteResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
	Stream string `json:"stream_url"`
}

type StatusResponse struct {
	Job
}

type ResultResponse struct {
	JobID      string   `json:"job_id"`
	Status     string   `json:"status"`
	OutputURLs []string `json:"output_urls,omitempty"`
	Error      string   `json:"error,omitempty"`
}
