package receiver

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"traefik-connect/internal/model"
	"traefik-connect/internal/render"
	"traefik-connect/internal/util"
)

type persistedState struct {
	Snapshot  model.Snapshot `json:"snapshot"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type workerRecord struct {
	persistedState
	RenderFile string
}

type Store struct {
	mu        sync.RWMutex
	stateDir  string
	renderDir string
	ttl       time.Duration
	log       *slog.Logger
	workers   map[string]workerRecord
}

func NewStore(stateDir, renderDir string, ttl time.Duration, log *slog.Logger) *Store {
	return &Store{
		stateDir:  stateDir,
		renderDir: renderDir,
		ttl:       ttl,
		log:       log,
		workers:   map[string]workerRecord{},
	}
}

func (s *Store) Load() error {
	if err := os.MkdirAll(s.stateDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(s.renderDir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.stateDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.stateDir, entry.Name()))
		if err != nil {
			if s.log != nil {
				s.log.Warn("failed to read persisted worker state", "file", entry.Name(), "error", err)
			}
			continue
		}
		var state persistedState
		if err := json.Unmarshal(raw, &state); err != nil {
			if s.log != nil {
				s.log.Warn("failed to decode persisted worker state", "file", entry.Name(), "error", err)
			}
			continue
		}
		workerID := state.Snapshot.WorkerID
		if workerID == "" {
			continue
		}
		s.workers[workerID] = workerRecord{persistedState: state, RenderFile: filepath.Join(s.renderDir, workerID+".yaml")}
	}
	return s.reconcileAll()
}

func (s *Store) Upsert(snapshot model.Snapshot) (model.Snapshot, []model.ValidationIssue, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	issues, cleaned := validateSnapshot(snapshot)
	if cleaned.WorkerID == "" {
		return model.Snapshot{}, issues, "", fmt.Errorf("worker id is required")
	}
	prev, ok := s.workers[cleaned.WorkerID]
	if ok && !cleaned.CapturedAt.After(prev.Snapshot.CapturedAt) {
		return model.Snapshot{}, issues, "", fmt.Errorf("replayed or stale snapshot")
	}

	if cleaned.Hash == "" {
		cleaned.Hash, _, _ = util.CanonicalHash(snapshotForHash(cleaned))
	}
	record := workerRecord{
		persistedState: persistedState{Snapshot: cleaned, UpdatedAt: time.Now().UTC()},
		RenderFile:     filepath.Join(s.renderDir, cleaned.WorkerID+".yaml"),
	}
	s.workers[cleaned.WorkerID] = record
	if err := s.persistLocked(record); err != nil {
		return model.Snapshot{}, issues, "", err
	}
	if len(cleaned.Containers) == 0 {
		_ = os.Remove(record.RenderFile)
		return cleaned, issues, record.RenderFile, nil
	}
	if err := s.renderLocked(record); err != nil {
		return model.Snapshot{}, issues, "", err
	}
	return cleaned, issues, record.RenderFile, nil
}

func (s *Store) RemoveExpired(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ttl <= 0 {
		return nil
	}
	for workerID, record := range s.workers {
		if now.Sub(record.UpdatedAt) <= s.ttl {
			continue
		}
		delete(s.workers, workerID)
		_ = os.Remove(filepath.Join(s.stateDir, workerID+".json"))
		_ = os.Remove(record.RenderFile)
	}
	return nil
}

func (s *Store) Statuses() []Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Status, 0, len(s.workers))
	for _, record := range s.workers {
		out = append(out, Status{
			WorkerID:       record.Snapshot.WorkerID,
			CapturedAt:     record.Snapshot.CapturedAt,
			UpdatedAt:      record.UpdatedAt,
			Hash:           record.Snapshot.Hash,
			ContainerCount: len(record.Snapshot.Containers),
			RenderFile:     record.RenderFile,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkerID < out[j].WorkerID })
	return out
}

func (s *Store) renderLocked(record workerRecord) error {
	body, err := render.RenderSnapshot(record.Snapshot)
	if err != nil {
		return err
	}
	return util.AtomicWriteFile(record.RenderFile, body, 0o644)
}

func (s *Store) persistLocked(record workerRecord) error {
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return util.AtomicWriteFile(filepath.Join(s.stateDir, record.Snapshot.WorkerID+".json"), body, 0o644)
}

func (s *Store) reconcileAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.workers {
		if len(record.Snapshot.Containers) == 0 {
			_ = os.Remove(record.RenderFile)
			continue
		}
		if err := s.renderLocked(record); err != nil {
			if s.log != nil {
				s.log.Warn("failed to render worker snapshot", "worker_id", record.Snapshot.WorkerID, "error", err)
			}
			continue
		}
	}
	return nil
}

type Status struct {
	WorkerID       string    `json:"worker_id"`
	CapturedAt     time.Time `json:"captured_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Hash           string    `json:"hash"`
	ContainerCount int       `json:"container_count"`
	RenderFile     string    `json:"render_file"`
}

func snapshotForHash(snapshot model.Snapshot) any {
	cp := snapshot
	cp.Hash = ""
	return cp
}

func validateSnapshot(snapshot model.Snapshot) ([]model.ValidationIssue, model.Snapshot) {
	cleaned := snapshot
	cleaned.Containers = nil
	issues := make([]model.ValidationIssue, 0)
	if snapshot.WorkerID == "" {
		issues = append(issues, model.ValidationIssue{Scope: "snapshot", Field: "worker_id", Message: "missing worker id"})
		return issues, cleaned
	}
	seen := map[string]struct{}{}
	for _, c := range snapshot.Containers {
		if c.ID == "" {
			issues = append(issues, model.ValidationIssue{WorkerID: snapshot.WorkerID, Container: c.Name, ContainerID: c.ID, Scope: "container", Field: "id", Message: "missing container id"})
			continue
		}
		if _, ok := seen[c.ID]; ok {
			issues = append(issues, model.ValidationIssue{WorkerID: snapshot.WorkerID, Container: c.Name, ContainerID: c.ID, Scope: "container", Field: "id", Message: "duplicate container id"})
			continue
		}
		seen[c.ID] = struct{}{}
		if c.Name == "" {
			issues = append(issues, model.ValidationIssue{WorkerID: snapshot.WorkerID, ContainerID: c.ID, Scope: "container", Field: "name", Message: "missing container name"})
			continue
		}
		if len(c.Routers) == 0 || len(c.Services) == 0 {
			issues = append(issues, model.ValidationIssue{WorkerID: snapshot.WorkerID, ContainerID: c.ID, Container: c.Name, Scope: "container", Field: "routes", Message: "missing routers or services"})
			continue
		}
		cleaned.Containers = append(cleaned.Containers, c)
	}
	return issues, cleaned
}
