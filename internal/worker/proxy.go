package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"example.com/traefik-connect/internal/model"
	"example.com/traefik-connect/internal/proxyheaders"
	"example.com/traefik-connect/internal/runtimeinfo"
	"example.com/traefik-connect/internal/tunnel"
)

type ProxyServer struct {
	agent *Agent
	log   *slog.Logger
	mux   *http.ServeMux
}

func (s *ProxyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *ProxyServer) Handler() http.Handler { return s.mux }

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
	ps.mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(runtimeinfo.Current("agent-proxy"))
	})
	ps.mux.HandleFunc("/tunnel", ps.handleTunnel)
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

func (s *ProxyServer) handleTunnel(w http.ResponseWriter, r *http.Request) {
	if !isWebSocketUpgrade(r.Header) {
		http.Error(w, "upgrade required", http.StatusUpgradeRequired)
		return
	}
	if token := r.Header.Get(proxyheaders.Token); token != s.agent.cfg.Token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	containerID := r.Header.Get(proxyheaders.ContainerID)
	serviceName := r.Header.Get(proxyheaders.ServiceName)
	if containerID == "" || serviceName == "" {
		http.Error(w, "missing tunnel metadata", http.StatusBadRequest)
		return
	}
	s.log.Info("tunnel phase", "phase", "accept", "container_id", containerID, "service_name", serviceName)

	stream, err := tunnel.Accept(w, r)
	if err != nil {
		s.log.Warn("failed to accept tunnel", "error", err)
		return
	}
	defer stream.Close()

	start, err := stream.ReadRequestStart()
	if err != nil {
		s.log.Warn("failed to read tunnel request", "error", err)
		return
	}
	s.log.Info("tunnel phase", "phase", "request_start", "method", start.Method, "path", start.Path, "query", start.RawQuery, "container_id", containerID, "service_name", serviceName)

	container, service, ok := s.agent.lookupService(containerID, serviceName)
	if !ok {
		_ = stream.WriteResponseStart(tunnel.ResponseStart{
			Status: http.StatusNotFound,
			Header: http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
		})
		_ = stream.WriteBinary([]byte("service not found\n"))
		_ = stream.WriteClose(nil)
		return
	}
	s.log.Info("tunnel phase", "phase", "lookup_service", "backend_url", service.BackendURL, "container_id", containerID, "service_name", serviceName)

	if isWebSocketUpgrade(start.Header) {
		if err := s.handleWebSocketTunnel(stream, start, container, service); err != nil {
			s.log.Warn("websocket tunnel failed", "container_id", containerID, "service_name", serviceName, "error", err)
		}
		return
	}

	if err := s.handleHTTPTunnel(r.Context(), stream, start, container, service); err != nil {
		s.log.Warn("http tunnel failed", "container_id", containerID, "service_name", serviceName, "error", err)
	}
}

func (s *ProxyServer) handleHTTPTunnel(ctx context.Context, stream *tunnel.Stream, start tunnel.RequestStart, container model.ContainerSpec, service model.ServiceSpec) error {
	target, err := serviceURL(service.BackendURL, start.Path, start.RawQuery)
	if err != nil {
		return err
	}
	u, err := url.Parse(target)
	if err != nil {
		return err
	}
	host := u.Host
	if host == "" {
		return fmt.Errorf("backend url missing host")
	}
	conn, err := net.DialTimeout("tcp", host, 30*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()

	chunked := start.ContentLength < 0
	if err := writeHTTPRequest(conn, start, u, service.PassHostHeader != nil && *service.PassHostHeader, false, chunked); err != nil {
		return err
	}
	s.log.Info("tunnel phase", "phase", "backend_request", "target", target, "container_id", container.ID, "service_name", service.Name)

	bodyDone := make(chan error, 1)
	if start.ContentLength != 0 {
		go func() {
			bodyDone <- pumpTunnelRequestBodyToConn(stream, conn, chunked)
		}()
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: start.Method})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	s.log.Info("tunnel phase", "phase", "response_start", "status", resp.StatusCode, "target", target, "container_id", container.ID, "service_name", service.Name)
	if err := stream.WriteResponseStart(tunnel.ResponseStart{
		Status: resp.StatusCode,
		Header: sanitizeResponseHeaders(resp.Header),
	}); err != nil {
		return err
	}
	if err := copyResponseToTunnel(stream, resp.Body); err != nil {
		return err
	}
	s.log.Info("tunnel phase", "phase", "response_body", "target", target, "container_id", container.ID, "service_name", service.Name)
	if err := stream.WriteClose(nil); err != nil {
		return err
	}
	if start.ContentLength != 0 {
		return <-bodyDone
	}
	return nil
}

func (s *ProxyServer) handleWebSocketTunnel(stream *tunnel.Stream, start tunnel.RequestStart, container model.ContainerSpec, service model.ServiceSpec) error {
	target, err := serviceURL(service.BackendURL, start.Path, start.RawQuery)
	if err != nil {
		return err
	}
	u, err := url.Parse(target)
	if err != nil {
		return err
	}
	host := u.Host
	if host == "" {
		return fmt.Errorf("backend url missing host")
	}
	conn, err := net.DialTimeout("tcp", host, 30*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := writeHTTPRequest(conn, start, u, service.PassHostHeader != nil && *service.PassHostHeader, true, false); err != nil {
		return err
	}
	s.log.Info("tunnel phase", "phase", "backend_request", "target", target, "container_id", container.ID, "service_name", service.Name)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: start.Method})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := stream.WriteResponseStart(tunnel.ResponseStart{
		Status: resp.StatusCode,
		Header: sanitizeResponseHeaders(resp.Header),
	}); err != nil {
		return err
	}
	s.log.Info("tunnel phase", "phase", "response_start", "status", resp.StatusCode, "target", target, "container_id", container.ID, "service_name", service.Name)
	if resp.StatusCode != http.StatusSwitchingProtocols {
		if err := copyResponseToTunnel(stream, resp.Body); err != nil {
			return err
		}
		return stream.WriteClose(nil)
	}
	s.log.Info("tunnel phase", "phase", "upgrade", "target", target, "container_id", container.ID, "service_name", service.Name)

	return relayTunnelAndConn(stream, conn, br)
}

func relayTunnelAndConn(stream *tunnel.Stream, conn net.Conn, br *bufio.Reader) error {
	errCh := make(chan error, 2)
	go func() {
		for {
			op, payload, err := stream.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			switch op {
			case 0x2:
				if len(payload) == 0 {
					continue
				}
				if _, err := conn.Write(payload); err != nil {
					errCh <- err
					return
				}
			case 0x8:
				errCh <- io.EOF
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, 32*1024)
		for {
			var (
				n   int
				err error
			)
			if br != nil {
				n, err = br.Read(buf)
			} else {
				n, err = conn.Read(buf)
			}
			if n > 0 {
				if err := stream.WriteBinary(buf[:n]); err != nil {
					errCh <- err
					return
				}
			}
			if err != nil {
				errCh <- err
				return
			}
		}
	}()
	return <-errCh
}

func pumpTunnelRequestBody(stream *tunnel.Stream, w *io.PipeWriter) error {
	defer w.Close()
	for {
		op, payload, err := stream.ReadMessage()
		if err != nil {
			return w.CloseWithError(err)
		}
		switch op {
		case 0x2:
			if len(payload) == 0 {
				continue
			}
			if _, err := w.Write(payload); err != nil {
				return w.CloseWithError(err)
			}
		case 0x8:
			return nil
		default:
			continue
		}
	}
}

func pumpTunnelRequestBodyToConn(stream *tunnel.Stream, conn net.Conn, chunked bool) error {
	for {
		op, payload, err := stream.ReadMessage()
		if err != nil {
			return err
		}
		switch op {
		case 0x2:
			if len(payload) == 0 {
				continue
			}
			if chunked {
				if err := writeHTTPChunk(conn, payload); err != nil {
					return err
				}
				continue
			}
			if _, err := conn.Write(payload); err != nil {
				return err
			}
		case 0x8:
			if chunked {
				_, err := io.WriteString(conn, "0\r\n\r\n")
				return err
			}
			return nil
		}
	}
}

func copyResponseToTunnel(stream *tunnel.Stream, body io.Reader) error {
	buf := make([]byte, 32*1024)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if err := stream.WriteBinary(buf[:n]); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func serviceURL(base, path, rawQuery string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u.Path = joinPath(u.Path, path)
	u.RawQuery = rawQuery
	return u.String(), nil
}

func joinPath(prefix, suffix string) string {
	if prefix == "" {
		prefix = "/"
	}
	if suffix == "" {
		return prefix
	}
	if strings.HasSuffix(prefix, "/") {
		prefix = strings.TrimSuffix(prefix, "/")
	}
	if !strings.HasPrefix(suffix, "/") {
		suffix = "/" + suffix
	}
	return prefix + suffix
}

func sanitizeHeaders(in http.Header) http.Header {
	out := make(http.Header, len(in))
	for k, vals := range in {
		if isHopByHopHeader(k) {
			continue
		}
		for _, v := range vals {
			out.Add(k, v)
		}
	}
	return out
}

func sanitizeResponseHeaders(in http.Header) http.Header {
	out := make(http.Header, len(in))
	for k, vals := range in {
		if isHopByHopHeader(k) {
			continue
		}
		for _, v := range vals {
			out.Add(k, v)
		}
	}
	return out
}

func isHopByHopHeader(key string) bool {
	switch strings.ToLower(key) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func isWebSocketUpgrade(h http.Header) bool {
	return strings.Contains(strings.ToLower(h.Get("Connection")), "upgrade") &&
		strings.EqualFold(h.Get("Upgrade"), "websocket")
}

func writeHTTPRequest(conn net.Conn, start tunnel.RequestStart, backendURL *url.URL, passHostHeader bool, upgrade bool, chunked bool) error {
	path := start.Path
	if path == "" {
		path = "/"
	}
	if start.RawQuery != "" {
		path += "?" + start.RawQuery
	}
	host := backendURL.Host
	if passHostHeader && start.Host != "" {
		host = start.Host
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s HTTP/1.1\r\n", start.Method, path)
	fmt.Fprintf(&b, "Host: %s\r\n", host)
	for k, vals := range start.Header {
		if isHopByHopHeader(k) && !upgrade {
			continue
		}
		for _, v := range vals {
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	if chunked {
		_, _ = fmt.Fprintf(&b, "Transfer-Encoding: chunked\r\n")
	} else if start.ContentLength > 0 {
		fmt.Fprintf(&b, "Content-Length: %d\r\n", start.ContentLength)
	}
	b.WriteString("\r\n")
	_, err := conn.Write([]byte(b.String()))
	return err
}

func writeHTTPChunk(conn net.Conn, payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(conn, "%x\r\n", len(payload)); err != nil {
		return err
	}
	if _, err := conn.Write(payload); err != nil {
		return err
	}
	_, err := io.WriteString(conn, "\r\n")
	return err
}
