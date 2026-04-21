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
	"time"

	"example.com/traefik-connect/internal/api"
	"example.com/traefik-connect/internal/model"
)

type ProxyServer struct {
	agent *Agent
	log   *slog.Logger
	mux   *http.ServeMux
}

func NewProxyServer(agent *Agent, log *slog.Logger) *ProxyServer {
	ps := &ProxyServer{
		agent: agent,
		log:   log,
		mux:   http.NewServeMux(),
	}
	ps.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	ps.mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	ps.mux.HandleFunc("/v1/proxy", ps.handleProxy)
	return ps
}

func (s *ProxyServer) Listen(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.mux,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		<-errCh
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

func (s *ProxyServer) handleProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !bearerOK(r, s.agent.cfg.Token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req api.ProxyRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid json: %v", err), http.StatusBadRequest)
		return
	}
	resp, err := s.agent.proxyRequest(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, fmt.Sprintf("encode proxy response: %v", err), http.StatusInternalServerError)
		return
	}
}

func bearerOK(r *http.Request, token string) bool {
	auth := r.Header.Get("Authorization")
	return token != "" && len(auth) > 7 && auth[:7] == "Bearer " && auth[7:] == token
}

func (a *Agent) proxyRequest(ctx context.Context, req api.ProxyRequest) (api.ProxyResponse, error) {
	snapshot := a.latestSnapshot()
	if snapshot.WorkerID == "" {
		return api.ProxyResponse{}, errors.New("no active snapshot")
	}
	svc, _, err := a.lookupService(snapshot, req.ContainerID, req.ServiceName)
	if err != nil {
		return api.ProxyResponse{}, err
	}
	target, err := url.Parse(svc.BackendURL)
	if err != nil {
		return api.ProxyResponse{}, err
	}
	target.Path = req.Path
	target.RawQuery = req.RawQuery
	outReq, err := http.NewRequestWithContext(ctx, req.Method, target.String(), bytes.NewReader(req.Body))
	if err != nil {
		return api.ProxyResponse{}, err
	}
	for k, values := range req.Header {
		if hopByHopHeader(k) {
			continue
		}
		for _, v := range values {
			outReq.Header.Add(k, v)
		}
	}
	if svc.PassHostHeader != nil && *svc.PassHostHeader && req.Host != "" {
		outReq.Host = req.Host
	} else {
		outReq.Host = target.Host
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(outReq)
	if err != nil {
		return api.ProxyResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return api.ProxyResponse{}, err
	}
	return api.ProxyResponse{
		StatusCode: resp.StatusCode,
		Header:     cloneHeader(resp.Header),
		Body:       body,
	}, nil
}

func cloneHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, values := range h {
		for _, v := range values {
			out.Add(k, v)
		}
	}
	return out
}

func hopByHopHeader(key string) bool {
	switch http.CanonicalHeaderKey(key) {
	case "Connection", "Proxy-Connection", "Keep-Alive", "Transfer-Encoding", "Te", "Trailer", "Upgrade":
		return true
	default:
		return false
	}
}

func (a *Agent) latestSnapshot() model.Snapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.snapshot
}

func (a *Agent) lookupService(snapshot model.Snapshot, containerID, serviceName string) (model.ServiceSpec, model.ContainerSpec, error) {
	for _, c := range snapshot.Containers {
		if c.ID != containerID {
			continue
		}
		svc, ok := c.Services[serviceName]
		if !ok {
			return model.ServiceSpec{}, model.ContainerSpec{}, fmt.Errorf("unknown service %q", serviceName)
		}
		return svc, c, nil
	}
	return model.ServiceSpec{}, model.ContainerSpec{}, fmt.Errorf("unknown container %q", containerID)
}
