package worker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/traefik-connect/internal/config"
	"example.com/traefik-connect/internal/model"
	"example.com/traefik-connect/internal/proxyheaders"
	"example.com/traefik-connect/internal/tunnel"
)

var workerLargePayload = "0123456789abcdef" + "x" + strings.Repeat("a", 1<<20)

func TestProxyServerTunnelStreamsRequestAndResponse(t *testing.T) {
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
	srv := httptest.NewServer(NewProxyServer(agent, slog.Default()))
	defer srv.Close()

	stream, err := tunnel.Dial(context.Background(), srv.URL+"/tunnel", http.Header{
		proxyheaders.Token:       []string{"secret"},
		proxyheaders.ContainerID: []string{"container-1"},
		proxyheaders.ServiceName: []string{"whoami-svc"},
	})
	if err != nil {
		t.Fatalf("dial tunnel: %v", err)
	}
	defer stream.Close()

	reqStart := tunnel.RequestStart{
		Method:        http.MethodPost,
		Path:          "/hello",
		RawQuery:      "a=1",
		Host:          "whoami.example.test",
		Header:        http.Header{"Authorization": []string{"Bearer client-token"}, "X-Test": []string{"value"}},
		ContentLength: int64(len(workerLargePayload)),
	}
	if err := stream.WriteRequestStart(reqStart); err != nil {
		t.Fatalf("write request start: %v", err)
	}
	if err := stream.WriteBinary([]byte(workerLargePayload[:512*1024])); err != nil {
		t.Fatalf("write body chunk 1: %v", err)
	}
	if err := stream.WriteBinary([]byte(workerLargePayload[512*1024:])); err != nil {
		t.Fatalf("write body chunk 2: %v", err)
	}
	if err := stream.WriteRequestEnd(); err != nil {
		t.Fatalf("write request end: %v", err)
	}

	respStart, err := stream.ReadResponseStart()
	if err != nil {
		t.Fatalf("read response start: %v", err)
	}
	if respStart.Status != http.StatusOK {
		t.Fatalf("status = %d", respStart.Status)
	}
	if got := respStart.Header.Get("X-Backend"); got != "ok" {
		t.Fatalf("response header X-Backend = %q", got)
	}

	var body bytes.Buffer
	for {
		opcode, payload, err := stream.ReadMessage()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("read response frame: %v", err)
		}
		if opcode == 0x8 {
			break
		}
		if opcode == 0x2 && len(payload) > 0 {
			_, _ = body.Write(payload)
		}
	}
	if got := body.String(); got != "proxied" {
		t.Fatalf("body = %q", got)
	}
}

func TestProxyServerVersionAndHealth(t *testing.T) {
	agent := &Agent{cfg: config.AgentConfig{Token: "secret"}}
	srv := httptest.NewServer(NewProxyServer(agent, slog.Default()))
	defer srv.Close()

	for _, path := range []string{"/healthz", "/readyz", "/version"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", path, resp.StatusCode)
		}
	}
}

func boolPtr(v bool) *bool { return &v }

func TestProxyServerHandlesLongPollingStyleStream(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("no flusher")
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, "waiting")
		flusher.Flush()
		time.Sleep(20 * time.Millisecond)
		_, _ = fmt.Fprintln(w, "done")
	}))
	defer backend.Close()

	agent := &Agent{
		cfg: config.AgentConfig{Token: "secret"},
		snapshot: model.Snapshot{
			WorkerID: "worker-a",
			Containers: []model.ContainerSpec{
				{
					ID: "container-1",
					Services: map[string]model.ServiceSpec{
						"whoami-svc": {BackendURL: backend.URL},
					},
				},
			},
		},
	}
	srv := httptest.NewServer(NewProxyServer(agent, slog.Default()))
	defer srv.Close()

	stream, err := tunnel.Dial(context.Background(), srv.URL+"/tunnel", http.Header{
		proxyheaders.Token:       []string{"secret"},
		proxyheaders.ContainerID: []string{"container-1"},
		proxyheaders.ServiceName: []string{"whoami-svc"},
	})
	if err != nil {
		t.Fatalf("dial tunnel: %v", err)
	}
	defer stream.Close()

	if err := stream.WriteRequestStart(tunnel.RequestStart{
		Method: http.MethodGet,
		Path:   "/wait",
		Host:   "stream.example.test",
		Header: http.Header{},
	}); err != nil {
		t.Fatalf("write request start: %v", err)
	}
	if err := stream.WriteRequestEnd(); err != nil {
		t.Fatalf("write request end: %v", err)
	}

	respStart, err := stream.ReadResponseStart()
	if err != nil {
		t.Fatalf("read response start: %v", err)
	}
	if respStart.Status != http.StatusOK {
		t.Fatalf("status = %d", respStart.Status)
	}

	var body strings.Builder
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for streaming body")
		default:
		}
		opcode, payload, err := stream.ReadMessage()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("read frame: %v", err)
		}
		if opcode == 0x8 {
			break
		}
		if opcode == 0x2 {
			body.Write(payload)
		}
	}
	if got := body.String(); !strings.Contains(got, "waiting") || !strings.Contains(got, "done") {
		t.Fatalf("body = %q", got)
	}
}
