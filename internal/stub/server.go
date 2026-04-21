package stub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"example.com/traefik-connect/internal/api"
	"example.com/traefik-connect/internal/config"
)

type Server struct {
	cfg    config.StubConfig
	base   *url.URL
	client *http.Client
	log    *slog.Logger
	mux    *http.ServeMux
}

func New(cfg config.StubConfig, log *slog.Logger) (*Server, error) {
	base, err := url.Parse(cfg.TargetURL)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:    cfg,
		base:   base,
		client: &http.Client{Timeout: 30 * time.Second},
		log:    log,
		mux:    http.NewServeMux(),
	}
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("/", s.handle)
	return s, nil
}

func (s *Server) Listen(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.ListenAddr,
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

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("read request: %v", err), http.StatusBadRequest)
		return
	}
	req := api.ProxyRequest{
		ContainerID: s.cfg.ContainerID,
		ServiceName: s.cfg.ServiceName,
		Method:      r.Method,
		Path:        r.URL.Path,
		RawQuery:    r.URL.RawQuery,
		Host:        r.Host,
		Header:      r.Header.Clone(),
		Body:        body,
	}
	delete(req.Header, "Authorization")
	delete(req.Header, "Connection")
	delete(req.Header, "Proxy-Connection")
	delete(req.Header, "Keep-Alive")
	delete(req.Header, "Transfer-Encoding")
	delete(req.Header, "Te")
	delete(req.Header, "Trailer")
	delete(req.Header, "Upgrade")
	encoded, err := json.Marshal(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("marshal request: %v", err), http.StatusBadRequest)
		return
	}
	proxyURL := *s.base
	proxyURL.Path = "/v1/proxy"
	outReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, proxyURL.String(), bytes.NewReader(encoded))
	if err != nil {
		http.Error(w, fmt.Sprintf("build upstream request: %v", err), http.StatusBadGateway)
		return
	}
	outReq.Header.Set("Authorization", "Bearer "+s.cfg.Token)
	outReq.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(outReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("upstream request failed: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		http.Error(w, string(data), resp.StatusCode)
		return
	}
	var proxyResp api.ProxyResponse
	if err := json.NewDecoder(resp.Body).Decode(&proxyResp); err != nil {
		http.Error(w, fmt.Sprintf("decode upstream response: %v", err), http.StatusBadGateway)
		return
	}
	for k, values := range proxyResp.Header {
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	if proxyResp.StatusCode == 0 {
		proxyResp.StatusCode = http.StatusOK
	}
	w.WriteHeader(proxyResp.StatusCode)
	if len(proxyResp.Body) > 0 {
		_, _ = w.Write(proxyResp.Body)
	}
}
