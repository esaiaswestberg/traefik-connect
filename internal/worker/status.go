package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type StatusServer struct {
	agent *Agent
	mux   *http.ServeMux
	srv   *http.Server
}

func NewStatusServer(agent *Agent) *StatusServer {
	ss := &StatusServer{
		agent: agent,
		mux:   http.NewServeMux(),
	}
	ss.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	ss.mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	ss.mux.HandleFunc("/debug/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ss.agent.Status())
	})
	return ss
}

func (s *StatusServer) Listen(ctx context.Context, addr string) error {
	s.srv = &http.Server{
		Addr:              addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutdownCtx)
		<-errCh
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}
