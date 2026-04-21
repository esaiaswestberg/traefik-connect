package api

import (
	"time"

	"example.com/traefik-connect/internal/model"
)

type SnapshotResponse struct {
	Accepted          bool                    `json:"accepted"`
	WorkerID          string                  `json:"worker_id"`
	Hash              string                  `json:"hash,omitempty"`
	ManagedContainers []string                `json:"managed_containers,omitempty"`
	Issues            []model.ValidationIssue `json:"issues,omitempty"`
	Message           string                  `json:"message,omitempty"`
}

type StatusResponse struct {
	Workers []WorkerStatus `json:"workers"`
}

type WorkerStatus struct {
	WorkerID          string    `json:"worker_id"`
	CapturedAt        time.Time `json:"captured_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	Hash              string    `json:"hash"`
	ContainerCount    int       `json:"container_count"`
	ManagedContainers []string  `json:"managed_containers,omitempty"`
}

type ProxyRequest struct {
	ContainerID string              `json:"container_id"`
	ServiceName string              `json:"service_name"`
	Method      string              `json:"method"`
	Path        string              `json:"path"`
	RawQuery    string              `json:"raw_query,omitempty"`
	Host        string              `json:"host,omitempty"`
	Header      map[string][]string `json:"header,omitempty"`
	Body        []byte              `json:"body,omitempty"`
}

type ProxyResponse struct {
	StatusCode int                 `json:"status_code"`
	Header     map[string][]string `json:"header,omitempty"`
	Body       []byte              `json:"body,omitempty"`
	Message    string              `json:"message,omitempty"`
}
