package stub

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"example.com/traefik-connect/internal/config"
	"example.com/traefik-connect/internal/proxyheaders"
	"example.com/traefik-connect/internal/runtimeinfo"
	"example.com/traefik-connect/internal/tunnel"
)

type Server struct {
	cfg config.StubConfig
	log *slog.Logger
	mux *http.ServeMux
}

func New(cfg config.StubConfig, log *slog.Logger) (*Server, error) {
	s := &Server{
		cfg: cfg,
		log: log,
		mux: http.NewServeMux(),
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
	if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/version" {
		return
	}
	if websocketUpgrade(r) {
		if err := s.handleUpgrade(w, r); err != nil && err != io.EOF {
			s.log.Warn("upgrade tunnel failed", "error", err)
		}
		return
	}
	if err := s.handleHTTP(w, r); err != nil && err != io.EOF {
		http.Error(w, fmt.Sprintf("proxy request failed: %v", err), http.StatusBadGateway)
	}
}

func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) error {
	s.log.Info("tunnel phase", "phase", "dial", "method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery)
	stream, err := tunnel.Dial(r.Context(), s.cfg.TargetURL, tunnelHeaders(s.cfg))
	if err != nil {
		return err
	}
	defer stream.Close()

	start := tunnel.RequestStart{
		Method:        r.Method,
		Path:          r.URL.Path,
		RawQuery:      r.URL.RawQuery,
		Host:          r.Host,
		Header:        sanitizeHeaders(r.Header, false),
		ContentLength: r.ContentLength,
	}
	if err := stream.WriteRequestStart(start); err != nil {
		return err
	}
	s.log.Info("tunnel phase", "phase", "request_start", "method", start.Method, "path", start.Path, "query", start.RawQuery)

	reqErrCh := make(chan error, 1)
	go func() {
		reqErrCh <- pumpBody(r.Body, stream)
	}()

	respStart, err := stream.ReadResponseStart()
	if err != nil {
		return err
	}
	s.log.Info("tunnel phase", "phase", "response_start", "status", respStart.Status, "path", r.URL.Path, "query", r.URL.RawQuery)
	if respStart.Status == http.StatusSwitchingProtocols {
		return s.handleUpgradedResponse(w, r, stream, respStart)
	}

	copyHeaders(w.Header(), respStart.Header, false)
	w.WriteHeader(respStart.Status)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	if err := copyTunnelBodyToWriter(stream, w, flusher); err != nil {
		return err
	}
	s.log.Info("tunnel phase", "phase", "response_body", "path", r.URL.Path, "query", r.URL.RawQuery)
	return <-reqErrCh
}

func (s *Server) handleUpgrade(w http.ResponseWriter, r *http.Request) error {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return fmt.Errorf("hijacking unsupported")
	}
	clientConn, buf, err := hj.Hijack()
	if err != nil {
		return err
	}
	defer clientConn.Close()

	stream, err := tunnel.Dial(r.Context(), s.cfg.TargetURL, tunnelHeaders(s.cfg))
	if err != nil {
		return err
	}
	defer stream.Close()

	start := tunnel.RequestStart{
		Method:        r.Method,
		Path:          r.URL.Path,
		RawQuery:      r.URL.RawQuery,
		Host:          r.Host,
		Header:        sanitizeHeaders(r.Header, true),
		ContentLength: r.ContentLength,
	}
	if err := stream.WriteRequestStart(start); err != nil {
		return err
	}
	s.log.Info("tunnel phase", "phase", "request_start", "method", start.Method, "path", start.Path, "query", start.RawQuery)
	if err := stream.WriteRequestEnd(); err != nil {
		return err
	}

	respStart, err := stream.ReadResponseStart()
	if err != nil {
		return err
	}
	s.log.Info("tunnel phase", "phase", "response_start", "status", respStart.Status, "path", r.URL.Path, "query", r.URL.RawQuery)
	if respStart.Status != http.StatusSwitchingProtocols {
		if _, err := fmt.Fprintf(buf, "HTTP/1.1 %d %s\r\n", respStart.Status, http.StatusText(respStart.Status)); err != nil {
			return err
		}
		copyHeadersToWriter(buf.Writer, respStart.Header, false)
		if _, err := fmt.Fprint(buf, "\r\n"); err != nil {
			return err
		}
		if err := buf.Flush(); err != nil {
			return err
		}
		return copyTunnelBodyToConn(stream, clientConn, nil)
	}

	if _, err := fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\n"); err != nil {
		return err
	}
	copyHeadersToWriter(buf.Writer, respStart.Header, true)
	if _, err := fmt.Fprint(buf, "\r\n"); err != nil {
		return err
	}
	if err := buf.Flush(); err != nil {
		return err
	}
	s.log.Info("tunnel phase", "phase", "upgrade", "path", r.URL.Path, "query", r.URL.RawQuery)

	return relayUpgrade(clientConn, stream)
}

func (s *Server) handleUpgradedResponse(w http.ResponseWriter, r *http.Request, stream *tunnel.Stream, respStart tunnel.ResponseStart) error {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return fmt.Errorf("hijacking unsupported")
	}
	clientConn, buf, err := hj.Hijack()
	if err != nil {
		return err
	}
	defer clientConn.Close()

	if _, err := fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\n"); err != nil {
		return err
	}
	copyHeadersToWriter(buf.Writer, respStart.Header, true)
	if _, err := fmt.Fprint(buf, "\r\n"); err != nil {
		return err
	}
	if err := buf.Flush(); err != nil {
		return err
	}
	s.log.Info("tunnel phase", "phase", "response_start", "status", respStart.Status, "path", r.URL.Path, "query", r.URL.RawQuery)
	return relayUpgrade(clientConn, stream)
}

func relayUpgrade(clientConn net.Conn, stream *tunnel.Stream) error {
	errCh := make(chan error, 2)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := clientConn.Read(buf)
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
	go func() {
		for {
			op, payload, err := stream.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			switch op {
			case 0x2:
				if len(payload) > 0 {
					if _, err := clientConn.Write(payload); err != nil {
						errCh <- err
						return
					}
				}
			case 0x8:
				errCh <- io.EOF
				return
			}
		}
	}()
	return <-errCh
}

func copyTunnelBodyToWriter(stream *tunnel.Stream, w http.ResponseWriter, flusher http.Flusher) error {
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
			if _, err := w.Write(payload); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		case 0x8:
			return io.EOF
		}
	}
}

func copyTunnelBodyToConn(stream *tunnel.Stream, conn net.Conn, flusher *bufio.Writer) error {
	_ = flusher
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
			if _, err := conn.Write(payload); err != nil {
				return err
			}
		case 0x8:
			return io.EOF
		}
	}
}

func pumpBody(body io.ReadCloser, stream *tunnel.Stream) error {
	defer body.Close()
	buf := make([]byte, 32*1024)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if err := stream.WriteBinary(buf[:n]); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return stream.WriteRequestEnd()
		}
		if err != nil {
			return err
		}
	}
}

func tunnelHeaders(cfg config.StubConfig) http.Header {
	h := http.Header{}
	h.Set(proxyheaders.Token, cfg.Token)
	h.Set(proxyheaders.ContainerID, cfg.ContainerID)
	h.Set(proxyheaders.ServiceName, cfg.ServiceName)
	return h
}

func sanitizeHeaders(in http.Header, upgrade bool) http.Header {
	out := make(http.Header, len(in))
	for k, vals := range in {
		if internalHeader(k) {
			continue
		}
		if !upgrade && hopByHopHeader(k) {
			continue
		}
		for _, v := range vals {
			out.Add(k, v)
		}
	}
	return out
}

func copyHeaders(dst http.Header, src http.Header, upgrade bool) {
	for k := range dst {
		delete(dst, k)
	}
	for k, vals := range src {
		if !upgrade && hopByHopHeader(k) {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func copyHeadersToWriter(w *bufio.Writer, src http.Header, upgrade bool) {
	for k, vals := range src {
		if !upgrade && hopByHopHeader(k) {
			continue
		}
		for _, v := range vals {
			_, _ = fmt.Fprintf(w, "%s: %s\r\n", k, v)
		}
	}
}

func internalHeader(key string) bool {
	switch http.CanonicalHeaderKey(key) {
	case http.CanonicalHeaderKey(proxyheaders.Token),
		http.CanonicalHeaderKey(proxyheaders.ContainerID),
		http.CanonicalHeaderKey(proxyheaders.ServiceName):
		return true
	}
	return false
}

func hopByHopHeader(key string) bool {
	switch strings.ToLower(key) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func websocketUpgrade(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}
