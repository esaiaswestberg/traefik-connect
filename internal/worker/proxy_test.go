package worker

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/traefik-connect/internal/config"
	"example.com/traefik-connect/internal/model"
	"example.com/traefik-connect/internal/proxyheaders"
)

var workerLargePayload = "0123456789abcdef" + "x" + strings.Repeat("a", 1<<20)

func TestProxyServerStreamsRequestAndResponse(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/hello" {
			t.Fatalf("backend path = %q", got)
		}
		if got := r.URL.RawQuery; got != "a=1" {
			t.Fatalf("backend query = %q", got)
		}
		if got := r.Header.Get("X-Test"); got != "value" {
			t.Fatalf("backend header = %q", got)
		}
		if got := r.Header.Get(proxyheaders.Token); got != "" {
			t.Fatalf("backend saw internal token header = %q", got)
		}
		if got := r.Header.Get(proxyheaders.ContainerID); got != "" {
			t.Fatalf("backend saw container id header = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer client-token" {
			t.Fatalf("backend authorization = %q", got)
		}
		if got := r.Host; got != "whoami.example.test" {
			t.Fatalf("backend host = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read backend body: %v", err)
		}
		if got := len(body); got != len(workerLargePayload) {
			t.Fatalf("backend body len = %d", got)
		}
		if got := string(body[:16]); got != workerLargePayload[:16] {
			t.Fatalf("backend body prefix = %q", got)
		}
		w.Header().Set("X-Backend", "ok")
		_, _ = w.Write([]byte("proxied"))
	}))
	defer backend.Close()

	agent := &Agent{
		cfg: config.AgentConfig{Token: "secret"},
		snapshot: model.Snapshot{
			WorkerID: "worker-a",
			Containers: []model.ContainerSpec{
				{
					ID:   "container-1",
					Name: "whoami",
					Services: map[string]model.ServiceSpec{
						"whoami-svc": {
							BackendURL:     backend.URL,
							PassHostHeader: boolPtr(true),
						},
					},
				},
			},
		},
	}
	srv := NewProxyServer(agent, slog.Default())

	req := httptest.NewRequest(http.MethodPost, "http://worker.local/hello?a=1", strings.NewReader(workerLargePayload))
	req.Host = "whoami.example.test"
	req.Header.Set("Authorization", "Bearer client-token")
	req.Header.Set("X-Test", "value")
	req.Header.Set(proxyheaders.Token, "secret")
	req.Header.Set(proxyheaders.ContainerID, "container-1")
	req.Header.Set(proxyheaders.ServiceName, "whoami-svc")
	rec := httptest.NewRecorder()

	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Backend"); got != "ok" {
		t.Fatalf("response header X-Backend = %q", got)
	}
	if got := rec.Body.String(); got != "proxied" {
		t.Fatalf("body = %q", got)
	}
}

func boolPtr(v bool) *bool { return &v }
