package testapp

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"example.com/traefik-connect/internal/config"
)

type Server struct {
	cfg      config.TestAppConfig
	log      *slog.Logger
	mux      *http.ServeMux
	filePath string
	modTime  time.Time
}

func New(cfg config.TestAppConfig, log *slog.Logger) (*Server, error) {
	filePath, modTime, err := createSampleFile(cfg.FileSize)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:      cfg,
		log:      log,
		mux:      http.NewServeMux(),
		filePath: filePath,
		modTime:  modTime,
	}
	s.routes()
	return s, nil
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("/upload", s.handleUpload)
	s.mux.HandleFunc("/events", s.handleEvents)
	s.mux.HandleFunc("/wait", s.handleWait)
	s.mux.HandleFunc("/ws", s.handleWebSocket)
	s.mux.HandleFunc("/file", s.handleFile)
	s.mux.HandleFunc("/", s.handleRoot)
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

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintf(w, "%s ok\n", s.cfg.Name)
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var total int64
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, fmt.Sprintf("read multipart upload: %v", err), http.StatusBadRequest)
			return
		}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, fmt.Sprintf("read multipart part: %v", err), http.StatusBadRequest)
				return
			}
			n, err := io.Copy(io.Discard, part)
			part.Close()
			if err != nil {
				http.Error(w, fmt.Sprintf("read multipart payload: %v", err), http.StatusBadRequest)
				return
			}
			total += n
		}
	} else {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("read upload: %v", err), http.StatusBadRequest)
			return
		}
		total = n
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintf(w, "uploaded=%d\n", total)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for i := 0; i < 10; i++ {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_, _ = fmt.Fprintf(w, "data: %s %d\n\n", s.cfg.Name, i)
			flusher.Flush()
		}
	}
}

func (s *Server) handleWait(w http.ResponseWriter, r *http.Request) {
	dur := 45 * time.Second
	if raw := r.URL.Query().Get("duration"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			dur = parsed
		}
	}
	timer := time.NewTimer(dur)
	defer timer.Stop()
	select {
	case <-r.Context().Done():
		return
	case <-timer.C:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, "waited %s\n", dur)
	}
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	f, err := os.Open(s.filePath)
	if err != nil {
		http.Error(w, fmt.Sprintf("open file: %v", err), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Disposition", `inline; filename="sample.bin"`)
	http.ServeContent(w, r, "sample.bin", s.modTime, f)
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !websocketUpgrade(r) {
		http.Error(w, "upgrade required", http.StatusUpgradeRequired)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing websocket key", http.StatusBadRequest)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		http.Error(w, fmt.Sprintf("hijack: %v", err), http.StatusInternalServerError)
		return
	}
	defer conn.Close()
	accept := websocketAccept(key)
	_, _ = fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\n")
	_, _ = fmt.Fprintf(buf, "Upgrade: websocket\r\n")
	_, _ = fmt.Fprintf(buf, "Connection: Upgrade\r\n")
	_, _ = fmt.Fprintf(buf, "Sec-WebSocket-Accept: %s\r\n\r\n", accept)
	if err := buf.Flush(); err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	_ = conn.SetWriteDeadline(time.Time{})
	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	for {
		opcode, payload, err := readFrame(br)
		if err != nil {
			return
		}
		switch opcode {
		case 0x8:
			_ = writeFrame(bw, 0x8, payload)
			_ = bw.Flush()
			return
		case 0x9:
			_ = writeFrame(bw, 0xA, payload)
			_ = bw.Flush()
		case 0x1, 0x2:
			if err := writeFrame(bw, opcode, payload); err != nil {
				return
			}
			if err := bw.Flush(); err != nil {
				return
			}
		default:
			continue
		}
	}
}

func createSampleFile(size int64) (string, time.Time, error) {
	dir, err := os.MkdirTemp("", "traefik-connect-testapp-*")
	if err != nil {
		return "", time.Time{}, err
	}
	path := filepath.Join(dir, "sample.bin")
	f, err := os.Create(path)
	if err != nil {
		return "", time.Time{}, err
	}
	defer f.Close()
	pattern := []byte("traefik-connect-testapp\n")
	written := int64(0)
	for written < size {
		n := int64(len(pattern))
		if remaining := size - written; remaining < n {
			n = remaining
		}
		if _, err := f.Write(pattern[:n]); err != nil {
			return "", time.Time{}, err
		}
		written += n
	}
	if err := f.Sync(); err != nil {
		return "", time.Time{}, err
	}
	if fi, err := f.Stat(); err == nil {
		return path, fi.ModTime(), nil
	}
	return path, time.Now().UTC(), nil
}

func websocketUpgrade(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func readFrame(r *bufio.Reader) (byte, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	opcode := header[0] & 0x0F
	masked := header[1]&0x80 != 0
	length := int64(header[1] & 0x7F)
	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(r, ext); err != nil {
			return 0, nil, err
		}
		length = int64(ext[0])<<8 | int64(ext[1])
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(r, ext); err != nil {
			return 0, nil, err
		}
		length = 0
		for _, b := range ext {
			length = (length << 8) | int64(b)
		}
	}
	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(r, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return opcode, payload, nil
}

func writeFrame(w *bufio.Writer, opcode byte, payload []byte) error {
	b0 := byte(0x80 | (opcode & 0x0F))
	if err := w.WriteByte(b0); err != nil {
		return err
	}
	switch {
	case len(payload) < 126:
		if err := w.WriteByte(byte(len(payload))); err != nil {
			return err
		}
	case len(payload) <= 0xFFFF:
		if err := w.WriteByte(126); err != nil {
			return err
		}
		if err := w.WriteByte(byte(len(payload) >> 8)); err != nil {
			return err
		}
		if err := w.WriteByte(byte(len(payload))); err != nil {
			return err
		}
	default:
		if err := w.WriteByte(127); err != nil {
			return err
		}
		for shift := 56; shift >= 0; shift -= 8 {
			if err := w.WriteByte(byte(uint64(len(payload)) >> shift)); err != nil {
				return err
			}
		}
	}
	_, err := w.Write(payload)
	return err
}
