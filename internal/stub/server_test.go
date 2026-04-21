package stub

import (
	"bufio"
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
	"example.com/traefik-connect/internal/websocketx"
	"example.com/traefik-connect/internal/worker"
)

var stubLargePayload = "0123456789abcdef" + "x" + strings.Repeat("a", 1<<20)

func TestServerStreamsAndAuthenticatesUpstream(t *testing.T) {
	stack := newTunnelTestStack(t)

	req, err := http.NewRequest(http.MethodPost, stack.stub.URL+"/hello?a=1", strings.NewReader(stubLargePayload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = "whoami.example.test"
	req.Header.Set("Authorization", "Bearer client-token")
	req.Header.Set("X-Test", "value")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Upstream"); got != "ok" {
		t.Fatalf("response header X-Upstream = %q", got)
	}
	if got := resp.Header.Get("X-Backend"); got != "ok" {
		t.Fatalf("response header X-Backend = %q", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if got := string(body); got != "proxied" {
		t.Fatalf("response body = %q", got)
	}
}

func TestServerWaitEventsAndWebSocket(t *testing.T) {
	stack := newTunnelTestStack(t)

	waitReq, err := http.NewRequest(http.MethodGet, stack.stub.URL+"/wait?duration=20ms", nil)
	if err != nil {
		t.Fatalf("new wait request: %v", err)
	}
	waitReq.Host = "stream.example.test"
	waitResp, err := http.DefaultClient.Do(waitReq)
	if err != nil {
		t.Fatalf("do wait request: %v", err)
	}
	defer waitResp.Body.Close()
	if waitResp.StatusCode != http.StatusOK {
		t.Fatalf("wait status = %d", waitResp.StatusCode)
	}
	waitBody, err := io.ReadAll(waitResp.Body)
	if err != nil {
		t.Fatalf("read wait body: %v", err)
	}
	if got := string(waitBody); !strings.Contains(got, "waiting 20ms") || !strings.Contains(got, "waited 20ms") {
		t.Fatalf("wait body = %q", got)
	}

	eventsReq, err := http.NewRequest(http.MethodGet, stack.stub.URL+"/events?count=2&interval=1ms", nil)
	if err != nil {
		t.Fatalf("new events request: %v", err)
	}
	eventsReq.Host = "stream.example.test"
	eventsResp, err := http.DefaultClient.Do(eventsReq)
	if err != nil {
		t.Fatalf("do events request: %v", err)
	}
	defer eventsResp.Body.Close()
	if eventsResp.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d", eventsResp.StatusCode)
	}
	eventsBody, err := io.ReadAll(eventsResp.Body)
	if err != nil {
		t.Fatalf("read events body: %v", err)
	}
	if got := string(eventsBody); !strings.Contains(got, ": testapp starting") || !strings.Contains(got, "data: testapp 0") {
		t.Fatalf("events body = %q", got)
	}

	ws, err := websocketx.Dial(context.Background(), stack.stub.URL+"/ws", http.Header{
		"Authorization": []string{"Bearer client-token"},
	})
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer ws.Close()
	if err := ws.WriteText([]byte("hello")); err != nil {
		t.Fatalf("write ws text: %v", err)
	}
	if err := ws.Flush(); err != nil {
		t.Fatalf("flush ws: %v", err)
	}
	opcode, payload, err := ws.ReadFrame()
	if err != nil {
		t.Fatalf("read ws frame: %v", err)
	}
	if opcode != 0x1 || string(payload) != "hello" {
		t.Fatalf("ws echo opcode=%d payload=%q", opcode, payload)
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

	for _, path := range []string{"/healthz", "/readyz", "/version"} {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://stub.local%s", path), nil)
		rec := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
	}
}

type tunnelTestStack struct {
	backend *httptest.Server
	worker  *httptest.Server
	stub    *httptest.Server
}

func newTunnelTestStack(t *testing.T) *tunnelTestStack {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hello":
			if got := r.URL.RawQuery; got != "a=1" {
				t.Fatalf("backend query = %q", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer client-token" {
				t.Fatalf("backend authorization = %q", got)
			}
			if got := r.Header.Get("X-Test"); got != "value" {
				t.Fatalf("backend x-test = %q", got)
			}
			if got := r.Host; got != "whoami.example.test" {
				t.Fatalf("backend host = %q", got)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("backend read body: %v", err)
			}
			if got := len(body); got != len(stubLargePayload) {
				t.Fatalf("backend body len = %d", got)
			}
			w.Header().Set("X-Backend", "ok")
			w.Header().Set("X-Upstream", "ok")
			_, _ = w.Write([]byte("proxied"))
		case "/wait":
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("backend missing flusher")
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = fmt.Fprintln(w, "waiting 20ms")
			flusher.Flush()
			time.Sleep(20 * time.Millisecond)
			_, _ = fmt.Fprintln(w, "waited 20ms")
			flusher.Flush()
		case "/events":
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("backend missing flusher")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			_, _ = fmt.Fprint(w, ": testapp starting\n\n")
			flusher.Flush()
			for i := 0; i < 2; i++ {
				_, _ = fmt.Fprintf(w, "data: testapp %d\n\n", i)
				flusher.Flush()
			}
		case "/ws":
			conn, err := websocketx.Accept(w, r)
			if err != nil {
				t.Fatalf("backend websocket accept: %v", err)
			}
			defer conn.Close()
			for {
				opcode, payload, err := conn.ReadFrame()
				if err != nil {
					return
				}
				switch opcode {
				case 0x8:
					_ = conn.WriteClose(payload)
					return
				case 0x1, 0x2:
					if err := conn.WriteText(payload); err != nil {
						return
					}
				}
			}
		default:
			t.Fatalf("unexpected backend path %s", r.URL.Path)
		}
	}))
	workerAgent, err := worker.NewAgent(config.AgentConfig{
		WorkerID:  "worker-a",
		MasterURL: "http://example.invalid",
		Token:     "secret-token",
	}, slog.Default())
	if err != nil {
		t.Fatalf("new worker agent: %v", err)
	}
	workerAgent.SetSnapshot(model.Snapshot{
		WorkerID: "worker-a",
		Containers: []model.ContainerSpec{
			{
				ID:   "abc123",
				Name: "whoami",
				Services: map[string]model.ServiceSpec{
					"whoami": {BackendURL: backend.URL, PassHostHeader: boolPtr(true)},
				},
			},
		},
	})
	workerSrv := httptest.NewServer(worker.NewProxyServer(workerAgent, slog.Default()))
	stubSrv, err := New(config.StubConfig{
		ListenAddr:  ":0",
		TargetURL:   workerSrv.URL + "/tunnel",
		Token:       "secret-token",
		ContainerID: "abc123",
		ServiceName: "whoami",
	}, slog.Default())
	if err != nil {
		t.Fatalf("new stub: %v", err)
	}
	stubHTTP := httptest.NewServer(stubSrv.mux)

	t.Cleanup(func() {
		stubHTTP.Close()
		workerSrv.Close()
		backend.Close()
	})

	return &tunnelTestStack{
		backend: backend,
		worker:  workerSrv,
		stub:    stubHTTP,
	}
}

func boolPtr(v bool) *bool { return &v }

// The testapp behavior is now exercised end-to-end through the tunnel path,
// so the old recorder-based unit tests are intentionally gone.
func TestTunnelStackHealth(t *testing.T) {
	stack := newTunnelTestStack(t)
	resp, err := http.Get(stack.stub.URL + "/healthz")
	if err != nil {
		t.Fatalf("health request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", resp.StatusCode)
	}
}

func TestTunnelStackBodyPassthrough(t *testing.T) {
	stack := newTunnelTestStack(t)
	req, err := http.NewRequest(http.MethodPost, stack.stub.URL+"/hello?a=1", bytes.NewBufferString(stubLargePayload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = "whoami.example.test"
	req.Header.Set("Authorization", "Bearer client-token")
	req.Header.Set("X-Test", "value")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got := string(body); got != "proxied" {
		t.Fatalf("body = %q", got)
	}
}

func TestTunnelStackStreamBody(t *testing.T) {
	stack := newTunnelTestStack(t)
	resp, err := http.Get(stack.stub.URL + "/wait?duration=20ms")
	if err != nil {
		t.Fatalf("wait request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "waiting 20ms") || !strings.Contains(got, "waited 20ms") {
		t.Fatalf("body = %q", got)
	}
}
