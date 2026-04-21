package receiver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/traefik-connect/internal/model"
)

func TestStoreUpsertRendersSnapshot(t *testing.T) {
	stateDir := t.TempDir()
	renderDir := t.TempDir()
	store := NewStore(stateDir, renderDir, time.Hour, nil)
	snapshot := model.Snapshot{
		WorkerID:   "worker-a",
		CapturedAt: time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		Version:    "v1",
		Containers: []model.ContainerSpec{
			{
				ID:   "abcdef1234567890",
				Name: "web",
				Routers: map[string]model.RouterSpec{
					"web": {Rule: "Host(`web.example.test`)", Service: "websvc"},
				},
				Services: map[string]model.ServiceSpec{
					"websvc": {BackendURL: "http://192.168.1.10:18080"},
				},
			},
		},
	}

	stored, issues, file, err := store.Upsert(snapshot)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("expected no issues, got %v", issues)
	}
	if file == "" {
		t.Fatal("expected render file")
	}
	data, err := os.ReadFile(filepath.Join(renderDir, "worker-a.yaml"))
	if err != nil {
		t.Fatalf("read render file: %v", err)
	}
	if !strings.Contains(string(data), "websvc") {
		t.Fatalf("render missing service: %s", string(data))
	}
	if stored.Hash == "" {
		t.Fatal("expected hash")
	}
}

func TestStoreUpsertSkipsInvalidContainers(t *testing.T) {
	stateDir := t.TempDir()
	renderDir := t.TempDir()
	store := NewStore(stateDir, renderDir, time.Hour, nil)
	snapshot := model.Snapshot{
		WorkerID:   "worker-a",
		CapturedAt: time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		Version:    "v1",
		Containers: []model.ContainerSpec{
			{
				ID:   "badbadbadbad",
				Name: "bad",
			},
			{
				ID:   "abcdef1234567890",
				Name: "web",
				Routers: map[string]model.RouterSpec{
					"web": {Rule: "Host(`web.example.test`)", Service: "websvc"},
				},
				Services: map[string]model.ServiceSpec{
					"websvc": {BackendURL: "http://192.168.1.10:18080"},
				},
			},
		},
	}

	_, issues, _, err := store.Upsert(snapshot)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if len(issues) == 0 {
		t.Fatal("expected validation issues")
	}
	data, err := os.ReadFile(filepath.Join(renderDir, "worker-a.yaml"))
	if err != nil {
		t.Fatalf("read render file: %v", err)
	}
	if strings.Contains(string(data), "badbadbadbad") {
		t.Fatalf("render should exclude invalid container: %s", string(data))
	}
}
