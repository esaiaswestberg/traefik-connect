package api

import (
	"time"

	"example.com/traefik-connect/internal/model"
)

type SnapshotResponse struct {
	Accepted     bool                    `json:"accepted"`
	WorkerID     string                  `json:"worker_id"`
	Hash         string                  `json:"hash,omitempty"`
	RenderedFile string                  `json:"rendered_file,omitempty"`
	Issues       []model.ValidationIssue `json:"issues,omitempty"`
	Message      string                  `json:"message,omitempty"`
}

type StatusResponse struct {
	Workers []WorkerStatus `json:"workers"`
}

type WorkerStatus struct {
	WorkerID       string    `json:"worker_id"`
	CapturedAt     time.Time `json:"captured_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Hash           string    `json:"hash"`
	ContainerCount int       `json:"container_count"`
	RenderFile     string    `json:"render_file"`
}
