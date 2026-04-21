package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"example.com/traefik-connect/internal/proxyheaders"
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
	ps.mux.HandleFunc("/", ps.handleProxy)
	return ps
}

func (s *ProxyServer) Listen(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.mux,
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
	if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
		return
	}
	if !tokenOK(r, s.agent.cfg.Token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	containerID := r.Header.Get(proxyheaders.ContainerID)
	serviceName := r.Header.Get(proxyheaders.ServiceName)
	if containerID == "" || serviceName == "" {
		http.Error(w, "missing proxy routing metadata", http.StatusBadRequest)
		return
	}
	proxy, err := s.agent.reverseProxyFor(containerID, serviceName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	proxy.ServeHTTP(w, r)
}

func tokenOK(r *http.Request, token string) bool {
	return token != "" && r.Header.Get(proxyheaders.Token) == token
}

func (a *Agent) reverseProxyFor(containerID, serviceName string) (*httputil.ReverseProxy, error) {
	snapshot := a.latestSnapshot()
	if snapshot.WorkerID == "" {
		return nil, errors.New("no active snapshot")
	}
	svc, _, err := a.lookupService(snapshot, containerID, serviceName)
	if err != nil {
		return nil, err
	}
	target, err := url.Parse(svc.BackendURL)
	if err != nil {
		return nil, err
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	passHostHeader := svc.PassHostHeader != nil && *svc.PassHostHeader
	proxy.Director = func(req *http.Request) {
		originalHost := req.Host
		originalDirector(req)
		req.Header.Del(proxyheaders.Token)
		req.Header.Del(proxyheaders.ContainerID)
		req.Header.Del(proxyheaders.ServiceName)
		if passHostHeader && originalHost != "" {
			req.Host = originalHost
		} else {
			req.Host = target.Host
		}
	}
	proxy.Transport = http.DefaultTransport
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, fmt.Sprintf("proxy backend failed: %v", err), http.StatusBadGateway)
	}
	return proxy, nil
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
