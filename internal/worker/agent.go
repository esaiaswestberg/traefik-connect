package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"example.com/traefik-connect/internal/api"
	"example.com/traefik-connect/internal/config"
	"example.com/traefik-connect/internal/dockerx"
	"example.com/traefik-connect/internal/model"
	"example.com/traefik-connect/internal/parse"
	"example.com/traefik-connect/internal/util"
)

type Agent struct {
	cfg     config.AgentConfig
	docker  *dockerx.Client
	client  *http.Client
	baseURL *url.URL
	log     *slog.Logger

	trigger chan struct{}

	mu     sync.RWMutex
	status AgentStatus
}

type AgentStatus struct {
	WorkerID       string    `json:"worker_id"`
	LastSyncAt     time.Time `json:"last_sync_at"`
	LastError      string    `json:"last_error,omitempty"`
	LastHash       string    `json:"last_hash,omitempty"`
	LastContainers int       `json:"last_containers"`
	LastSuccess    bool      `json:"last_success"`
}

func NewAgent(cfg config.AgentConfig, log *slog.Logger) (*Agent, error) {
	base, err := url.Parse(cfg.MasterURL)
	if err != nil {
		return nil, err
	}
	return &Agent{
		cfg:     cfg,
		docker:  dockerx.New(cfg.DockerSocket, 15*time.Second),
		client:  &http.Client{Timeout: cfg.RequestTimeout},
		baseURL: base,
		log:     log,
		trigger: make(chan struct{}, 1),
		status:  AgentStatus{WorkerID: cfg.WorkerID},
	}, nil
}

func (a *Agent) Run(ctx context.Context) error {
	statusServer := NewStatusServer(a)
	go func() {
		if err := statusServer.Listen(ctx, a.cfg.StatusListenAddr); err != nil && !errors.Is(err, context.Canceled) {
			a.log.Error("status server stopped", "error", err)
		}
	}()

	go a.watchEvents(ctx)
	a.signal()

	ticker := time.NewTicker(a.cfg.ResyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			a.signal()
		case <-a.trigger:
			a.syncWithRetry(ctx)
		}
	}
}

func (a *Agent) syncWithRetry(ctx context.Context) {
	backoff := 500 * time.Millisecond
	for {
		if err := a.syncOnce(ctx); err != nil {
			a.setError(err)
			a.log.Warn("sync failed", "error", err, "backoff", backoff.String())
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
			}
			continue
		}
		return
	}
}

func (a *Agent) syncOnce(ctx context.Context) error {
	containers, err := a.docker.ListContainers(ctx)
	if err != nil {
		return err
	}
	snapshot := model.Snapshot{
		WorkerID:      a.cfg.WorkerID,
		AdvertiseAddr: a.cfg.AdvertiseAddr,
		CapturedAt:    time.Now().UTC(),
		Version:       "v1",
		Containers:    make([]model.ContainerSpec, 0, len(containers)),
	}

	for _, summary := range containers {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ins, err := a.docker.InspectContainer(ctx, summary.ID)
		if err != nil {
			a.log.Warn("inspect failed", "container_id", summary.ID, "error", err)
			continue
		}
		spec, _, err := parse.BuildContainer(ins, a.cfg.WorkerID, a.cfg.AdvertiseAddr)
		if err != nil {
			a.log.Debug("skipping container", "container_id", ins.ID, "name", ins.Name, "reason", err.Error())
			continue
		}
		snapshot.Containers = append(snapshot.Containers, spec)
	}

	hash, _, err := util.CanonicalHash(snapshotForHash(snapshot))
	if err != nil {
		return err
	}
	snapshot.Hash = hash
	body, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL.JoinPath("/v1/snapshot").String(), io.NopCloser(bytes.NewReader(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	req.Header.Set(api.HeaderTimestamp, snapshot.CapturedAt.Format(time.RFC3339Nano))
	req.Header.Set(api.HeaderSignature, api.SignBody(a.cfg.Token, snapshot.CapturedAt, body))
	req.Header.Set(api.HeaderAgent, a.cfg.UserAgent)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var msg struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&msg)
		if msg.Message != "" {
			return fmt.Errorf("receiver rejected snapshot: %s", msg.Message)
		}
		return fmt.Errorf("receiver rejected snapshot: %s", resp.Status)
	}
	var result api.SnapshotResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	a.setSuccess(snapshot.Hash, len(snapshot.Containers))
	a.log.Info("snapshot synced", "worker_id", snapshot.WorkerID, "containers", len(snapshot.Containers), "hash", snapshot.Hash, "rendered_file", result.RenderedFile)
	return nil
}

func (a *Agent) watchEvents(ctx context.Context) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		err := a.docker.WatchEvents(ctx, time.Now().Add(-time.Minute), func(ev dockerx.Event) {
			switch ev.Action {
			case "start", "stop", "die", "destroy", "create", "rename", "restart", "update":
				a.signal()
			default:
				a.signal()
			}
		})
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		a.log.Warn("docker event stream ended", "error", err, "backoff", backoff.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (a *Agent) signal() {
	select {
	case a.trigger <- struct{}{}:
	default:
	}
}

func (a *Agent) setError(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status.LastSyncAt = time.Now().UTC()
	a.status.LastError = err.Error()
	a.status.LastSuccess = false
}

func (a *Agent) setSuccess(hash string, containers int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status.LastSyncAt = time.Now().UTC()
	a.status.LastHash = hash
	a.status.LastError = ""
	a.status.LastContainers = containers
	a.status.LastSuccess = true
}

func (a *Agent) Status() AgentStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.status
}

func snapshotForHash(snapshot model.Snapshot) any {
	cp := snapshot
	cp.Hash = ""
	return cp
}
