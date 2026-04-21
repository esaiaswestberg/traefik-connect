package testapp

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/traefik-connect/internal/config"
)

func TestServerUploadAndRangeFile(t *testing.T) {
	srv, err := New(config.TestAppConfig{
		ListenAddr: ":0",
		Name:       "testapp",
		FileSize:   1 << 20,
	}, slog.Default())
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	defer os.RemoveAll(filepath.Dir(srv.filePath))

	uploadReq := httptest.NewRequest(http.MethodPost, "http://testapp.local/upload", strings.NewReader(strings.Repeat("a", 1024)))
	uploadRec := httptest.NewRecorder()
	srv.mux.ServeHTTP(uploadRec, uploadReq)
	if uploadRec.Code != http.StatusOK {
		t.Fatalf("upload status = %d", uploadRec.Code)
	}
	if got := uploadRec.Body.String(); got != "uploaded=1024\n" {
		t.Fatalf("upload body = %q", got)
	}

	fileReq := httptest.NewRequest(http.MethodGet, "http://testapp.local/file", nil)
	fileReq.Header.Set("Range", "bytes=0-99")
	fileRec := httptest.NewRecorder()
	srv.mux.ServeHTTP(fileRec, fileReq)
	if fileRec.Code != http.StatusPartialContent {
		t.Fatalf("file status = %d", fileRec.Code)
	}
	if got := fileRec.Header().Get("Content-Range"); !strings.HasPrefix(got, "bytes 0-99/") {
		t.Fatalf("content-range = %q", got)
	}
	if got := len(fileRec.Body.Bytes()); got != 100 {
		t.Fatalf("range body len = %d", got)
	}
}

func TestServerWaitEndpoint(t *testing.T) {
	srv, err := New(config.TestAppConfig{
		ListenAddr: ":0",
		Name:       "testapp",
		FileSize:   1 << 20,
	}, slog.Default())
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	defer os.RemoveAll(filepath.Dir(srv.filePath))

	req := httptest.NewRequest(http.MethodGet, "http://testapp.local/wait?duration=10ms", nil)
	rec := httptest.NewRecorder()
	start := time.Now()
	srv.mux.ServeHTTP(rec, req)
	if time.Since(start) < 10*time.Millisecond {
		t.Fatalf("wait endpoint returned too early")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("wait status = %d", rec.Code)
	}
	got := rec.Body.String()
	if !strings.Contains(got, "waiting 10ms") {
		t.Fatalf("wait body missing initial chunk = %q", got)
	}
	if !strings.Contains(got, "waited 10ms") {
		t.Fatalf("wait body = %q", got)
	}
}
