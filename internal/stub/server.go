package stub

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"example.com/traefik-connect/internal/config"
	"example.com/traefik-connect/internal/proxyheaders"
	"example.com/traefik-connect/internal/runtimeinfo"
)

type Server struct {
	cfg   config.StubConfig
	base  *url.URL
	proxy *httputil.ReverseProxy
	log   *slog.Logger
	mux   *http.ServeMux
}

func New(cfg config.StubConfig, log *slog.Logger) (*Server, error) {
	base, err := url.Parse(cfg.TargetURL)
	if err != nil {
		return nil, err
	}
	proxy := httputil.NewSingleHostReverseProxy(base)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalHost := req.Host
		originalDirector(req)
		req.Host = originalHost
		req.Header.Del(proxyheaders.Token)
		req.Header.Set(proxyheaders.Token, cfg.Token)
		req.Header.Del(proxyheaders.ContainerID)
		req.Header.Set(proxyheaders.ContainerID, cfg.ContainerID)
		req.Header.Del(proxyheaders.ServiceName)
		req.Header.Set(proxyheaders.ServiceName, cfg.ServiceName)
	}
	proxy.Transport = http.DefaultTransport
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, fmt.Sprintf("proxy request failed: %v", err), http.StatusBadGateway)
	}
	s := &Server{
		cfg:   cfg,
		base:  base,
		proxy: proxy,
		log:   log,
		mux:   http.NewServeMux(),
	}
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(runtimeinfo.Current("stub"))
	})
	s.mux.HandleFunc("/", s.handle)
	return s, nil
}

func (s *Server) Listen(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.ListenAddr,
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

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
		return
	}
	s.proxy.ServeHTTP(w, r)
}
