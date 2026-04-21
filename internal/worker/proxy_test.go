package worker

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/traefik-connect/internal/api"
	"example.com/traefik-connect/internal/config"
	"example.com/traefik-connect/internal/model"
)

func TestProxyServerReturnsJSONEnvelope(t *testing.T) {
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
		if got := r.Host; got != "whoami.example.test" {
			t.Fatalf("backend host = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read backend body: %v", err)
		}
		if got := string(body); got != "payload" {
			t.Fatalf("backend body = %q", got)
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

	reqBody, _ := json.Marshal(api.ProxyRequest{
		ContainerID: "container-1",
		ServiceName: "whoami-svc",
		Method:      http.MethodPost,
		Path:        "/hello",
		RawQuery:    "a=1",
		Host:        "whoami.example.test",
		Header: map[string][]string{
			"Authorization": {"Bearer secret"},
			"X-Test":        {"value"},
		},
		Body: []byte("payload"),
	})
	req := httptest.NewRequest(http.MethodPost, "http://worker.local/v1/proxy", bytes.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()

	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content-type = %q", got)
	}
	var resp api.ProxyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode proxy response: %v", err)
	}
	if got := resp.StatusCode; got != http.StatusOK {
		t.Fatalf("status_code = %d", got)
	}
	if got := string(resp.Body); got != "proxied" {
		t.Fatalf("body = %q", got)
	}
	if got := resp.Header["X-Backend"]; len(got) != 1 || got[0] != "ok" {
		t.Fatalf("response header = %#v", resp.Header["X-Backend"])
	}
}

func boolPtr(v bool) *bool { return &v }
