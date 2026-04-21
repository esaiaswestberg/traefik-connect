package stub

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/traefik-connect/internal/config"
	"example.com/traefik-connect/internal/proxyheaders"
)

var stubLargePayload = "0123456789abcdef" + "x" + strings.Repeat("a", 1<<20)

func TestServerStreamsAndAuthenticatesUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(proxyheaders.Token); got != "secret-token" {
			t.Fatalf("upstream token = %q", got)
		}
		if got := r.Header.Get(proxyheaders.ContainerID); got != "abc123" {
			t.Fatalf("proxy container id = %q", got)
		}
		if got := r.Header.Get(proxyheaders.ServiceName); got != "whoami" {
			t.Fatalf("proxy service name = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer client-token" {
			t.Fatalf("client authorization = %q", got)
		}
		if got := r.URL.Path; got != "/hello" {
			t.Fatalf("upstream path = %q", got)
		}
		if got := r.URL.RawQuery; got != "a=1" {
			t.Fatalf("upstream query = %q", got)
		}
		if got := r.Host; got != "whoami.example.test" {
			t.Fatalf("upstream host = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		if got := len(body); got != len(stubLargePayload) {
			t.Fatalf("upstream body len = %d", got)
		}
		if got := string(body[:16]); got != stubLargePayload[:16] {
			t.Fatalf("upstream body prefix = %q", got)
		}
		w.Header().Set("X-Upstream", "ok")
		_, _ = w.Write([]byte("proxied"))
	}))
	defer upstream.Close()

	srv, err := New(config.StubConfig{
		ListenAddr:  ":0",
		TargetURL:   upstream.URL,
		Token:       "secret-token",
		ContainerID: "abc123",
		ServiceName: "whoami",
	}, slog.Default())
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "http://stub.local/hello?a=1", strings.NewReader(stubLargePayload))
	req.Host = "whoami.example.test"
	req.Header.Set("Authorization", "Bearer client-token")
	rec := httptest.NewRecorder()

	srv.handle(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Upstream"); got != "ok" {
		t.Fatalf("response header X-Upstream = %q", got)
	}
	if got := rec.Body.String(); got != "proxied" {
		t.Fatalf("response body = %q", got)
	}
}

func TestServerHealthEndpointsAreOpen(t *testing.T) {
	srv, err := New(config.StubConfig{
		ListenAddr:  ":0",
		TargetURL:   "http://example.invalid",
		Token:       "secret-token",
		ContainerID: "abc123",
		ServiceName: "whoami",
	}, slog.Default())
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	for _, path := range []string{"/healthz", "/readyz"} {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://stub.local%s", path), nil)
		rec := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
	}
}
