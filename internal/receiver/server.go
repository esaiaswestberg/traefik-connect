package receiver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"traefik-connect/internal/api"
	"traefik-connect/internal/model"
)

type Server struct {
	store        *Store
	token        string
	window       time.Duration
	maxBodyBytes int64
	mux          *http.ServeMux
}

func NewServer(store *Store, token string, window time.Duration, maxBodyBytes int64) *Server {
	s := &Server{
		store:        store,
		token:        token,
		window:       window,
		maxBodyBytes: maxBodyBytes,
		mux:          http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("/v1/snapshot", s.handleSnapshot)
	s.mux.HandleFunc("/v1/status", s.handleStatus)
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, ts, err := api.ValidateRequest(r, s.token, s.window, s.maxBodyBytes)
	if err != nil {
		code := http.StatusUnauthorized
		if errors.Is(err, context.DeadlineExceeded) {
			code = http.StatusGatewayTimeout
		} else if err.Error() == "request body too large" {
			code = http.StatusRequestEntityTooLarge
		} else if err.Error() == "unauthorized" || err.Error() == "invalid signature" || err.Error() == "missing timestamp" || err.Error() == "missing signature" {
			code = http.StatusUnauthorized
		} else {
			code = http.StatusBadRequest
		}
		http.Error(w, err.Error(), code)
		return
	}
	var snapshot model.Snapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		http.Error(w, fmt.Sprintf("invalid json: %v", err), http.StatusBadRequest)
		return
	}
	if snapshot.CapturedAt.IsZero() {
		snapshot.CapturedAt = ts
	}
	if !snapshot.CapturedAt.Equal(ts) {
		http.Error(w, "captured_at mismatch", http.StatusBadRequest)
		return
	}
	stored, issues, renderedFile, err := s.store.Upsert(snapshot)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := api.SnapshotResponse{
		Accepted:     true,
		WorkerID:     stored.WorkerID,
		Hash:         stored.Hash,
		RenderedFile: renderedFile,
		Issues:       issues,
		Message:      "snapshot accepted",
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp := api.StatusResponse{Workers: make([]api.WorkerStatus, 0)}
	for _, st := range s.store.Statuses() {
		resp.Workers = append(resp.Workers, api.WorkerStatus{
			WorkerID:       st.WorkerID,
			CapturedAt:     st.CapturedAt,
			UpdatedAt:      st.UpdatedAt,
			Hash:           st.Hash,
			ContainerCount: st.ContainerCount,
			RenderFile:     st.RenderFile,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
