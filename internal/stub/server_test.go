package stub

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/traefik-connect/internal/config"
)

func TestServerHandlesUnauthenticatedIngressAndAuthenticatesUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Fatalf("upstream authorization = %q", got)
		}
		if got := r.URL.Path; got != "/v1/proxy" {
			t.Fatalf("upstream path = %q", got)
		}
		var proxyReq struct {
			ContainerID string `json:"container_id"`
			ServiceName string `json:"service_name"`
			Method      string `json:"method"`
			Path        string `json:"path"`
			RawQuery    string `json:"raw_query"`
			Host        string `json:"host"`
			Body        []byte `json:"body"`
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		if err := json.Unmarshal(body, &proxyReq); err != nil {
			t.Fatalf("decode proxy request: %v", err)
		}
		if got := proxyReq.ContainerID; got != "abc123" {
			t.Fatalf("proxy container id = %q", got)
		}
		if got := proxyReq.ServiceName; got != "whoami" {
			t.Fatalf("proxy service name = %q", got)
		}
		if got := proxyReq.Method; got != http.MethodPost {
			t.Fatalf("proxy method = %q", got)
		}
		if got := proxyReq.Path; got != "/hello" {
			t.Fatalf("proxy path = %q", got)
		}
		if got := proxyReq.RawQuery; got != "a=1" {
			t.Fatalf("proxy query = %q", got)
		}
		if got := proxyReq.Host; got != "whoami.example.test" {
			t.Fatalf("proxy host = %q", got)
		}
		if got := string(proxyReq.Body); got != "payload" {
			t.Fatalf("proxy body = %q", got)
		}
		w.Header().Set("X-Upstream", "ok")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status_code":200,"header":{"X-Upstream":["ok"]},"body":"cHJveGllZA=="}`))
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

	req := httptest.NewRequest(http.MethodPost, "http://stub.local/hello?a=1", strings.NewReader("payload"))
	req.Host = "whoami.example.test"
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
