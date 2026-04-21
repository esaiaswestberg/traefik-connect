package receiver

import (
	"strings"
	"testing"
	"time"

	"example.com/traefik-connect/internal/model"
)

func TestBuildDesiredStubs(t *testing.T) {
	snapshot := model.Snapshot{
		WorkerID:      "worker-a",
		AdvertiseAddr: "192.168.1.10",
		ProxyPort:     8090,
		CapturedAt:    time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		Version:       "v2",
		Containers: []model.ContainerSpec{
			{
				ID:   "abcdef1234567890",
				Name: "web",
				Routers: map[string]model.RouterSpec{
					"web":       {Rule: "Host(`web.example.test`)", Service: "websvc", Middlewares: []string{"secure"}},
					"websecure": {Rule: "Host(`web.example.test`)", Service: "websvc", TLS: &model.TLSSpec{CertResolver: "letsencrypt"}},
				},
				Services: map[string]model.ServiceSpec{
					"websvc": {BackendURL: "http://172.18.0.5:18181", PassHostHeader: boolPtr(true)},
				},
				Middlewares: map[string]model.MiddlewareSpec{
					"secure": {RedirectScheme: &model.RedirectScheme{Scheme: "https", Permanent: boolPtr(true)}},
				},
			},
		},
	}

	specs, issues, err := buildDesiredStubs(snapshot, "traefik-connect", "traefik-connect", "sha256:stubimage", "secret")
	if err != nil {
		t.Fatalf("buildDesiredStubs() error = %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("expected no issues, got %v", issues)
	}
	if len(specs) != 1 {
		t.Fatalf("expected 1 stub, got %d", len(specs))
	}
	spec := specs[0]
	if spec.Labels["traefik.enable"] != "true" {
		t.Fatalf("expected traefik label, got %#v", spec.Labels)
	}
	if got := spec.Labels["traefik.http.routers.web.rule"]; got != "Host(`web.example.test`)" {
		t.Fatalf("router rule = %q", got)
	}
	if got := spec.Labels["traefik.http.services.websvc.loadbalancer.server.port"]; got != "18181" {
		t.Fatalf("service port = %q", got)
	}
	if got := spec.Labels["traefik.tcp.services.websvc.loadbalancer.server.port"]; got != "18181" {
		t.Fatalf("tcp service port = %q", got)
	}
	if got := spec.Labels["traefik.tcp.routers.websecure.rule"]; got != "HostSNI(`web.example.test`)" {
		t.Fatalf("tcp router rule = %q", got)
	}
	if got := spec.Labels["traefik.tcp.routers.websecure.tls.certresolver"]; got != "letsencrypt" {
		t.Fatalf("tcp cert resolver = %q", got)
	}
	if got := spec.Env[0]; got != "STUB_TARGET_URL=http://192.168.1.10:8090/tunnel" {
		t.Fatalf("stub target = %q", got)
	}
}

func TestBuildDesiredStubsWithPriority(t *testing.T) {
	priority := 50
	snapshot := model.Snapshot{
		WorkerID:      "worker-a",
		AdvertiseAddr: "192.168.1.10",
		ProxyPort:     8090,
		CapturedAt:    time.Now().UTC(),
		Version:       "v2",
		Containers: []model.ContainerSpec{
			{
				ID:   "abcdef",
				Name: "web",
				Routers: map[string]model.RouterSpec{
					"web":       {Rule: "Host(`web.test`)", Service: "svc", Priority: &priority},
					"websecure": {Rule: "Host(`web.test`)", Service: "svc", TLS: &model.TLSSpec{}, Priority: &priority},
				},
				Services: map[string]model.ServiceSpec{
					"svc": {BackendURL: "http://1.1.1.1:80"},
				},
			},
		},
	}

	specs, _, _ := buildDesiredStubs(snapshot, "net", "img", "id", "token")
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(specs))
	}
	labels := specs[0].Labels
	if got := labels["traefik.http.routers.web.priority"]; got != "50" {
		t.Errorf("http priority = %q, want 50", got)
	}
	if got := labels["traefik.tcp.routers.websecure.priority"]; got != "50" {
		t.Errorf("tcp priority = %q, want 50", got)
	}
}

func TestValidateSnapshotRequiresProxyMetadata(t *testing.T) {
	snapshot := model.Snapshot{
		WorkerID:   "worker-a",
		CapturedAt: time.Now().UTC(),
		Version:    "v2",
		Containers: []model.ContainerSpec{
			{
				ID:   "abcdef1234567890",
				Name: "web",
				Routers: map[string]model.RouterSpec{
					"web": {Rule: "Host(`web.example.test`)", Service: "websvc"},
				},
				Services: map[string]model.ServiceSpec{
					"websvc": {BackendURL: "http://172.18.0.5:18181"},
				},
			},
		},
	}

	issues, cleaned := validateSnapshot(snapshot)
	if len(issues) == 0 {
		t.Fatal("expected validation issues")
	}
	if cleaned.AdvertiseAddr != "" || cleaned.ProxyPort != 0 {
		t.Fatalf("expected snapshot metadata to remain unset in cleaned snapshot")
	}
}

func TestValidateSnapshotDropsInvalidContainers(t *testing.T) {
	snapshot := model.Snapshot{
		WorkerID:      "worker-a",
		AdvertiseAddr: "192.168.1.10",
		ProxyPort:     8090,
		CapturedAt:    time.Now().UTC(),
		Version:       "v2",
		Containers: []model.ContainerSpec{
			{ID: "bad", Name: "bad"},
			{
				ID:   "abcdef1234567890",
				Name: "web",
				Routers: map[string]model.RouterSpec{
					"web": {Rule: "Host(`web.example.test`)", Service: "websvc"},
				},
				Services: map[string]model.ServiceSpec{
					"websvc": {BackendURL: "http://172.18.0.5:18181"},
				},
			},
		},
	}

	issues, cleaned := validateSnapshot(snapshot)
	if len(issues) == 0 {
		t.Fatal("expected validation issues")
	}
	if len(cleaned.Containers) != 1 {
		t.Fatalf("expected one valid container, got %d", len(cleaned.Containers))
	}
	if !strings.Contains(cleaned.Containers[0].Name, "web") {
		t.Fatalf("unexpected cleaned container: %#v", cleaned.Containers[0])
	}
}

func TestPersistedRecordCompatible(t *testing.T) {
	cases := []struct {
		name   string
		record workerRecord
		want   bool
	}{
		{
			name: "legacy snapshot missing proxy port",
			record: workerRecord{
				persistedState: persistedState{
					Snapshot: model.Snapshot{
						WorkerID:      "worker-a",
						AdvertiseAddr: "192.168.1.10",
						ProxyPort:     0,
					},
				},
			},
			want: false,
		},
		{
			name: "valid snapshot",
			record: workerRecord{
				persistedState: persistedState{
					Snapshot: model.Snapshot{
						WorkerID:      "worker-a",
						AdvertiseAddr: "192.168.1.10",
						ProxyPort:     8090,
					},
				},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := persistedRecordCompatible(tc.record); got != tc.want {
				t.Fatalf("persistedRecordCompatible() = %v, want %v", got, tc.want)
			}
		})
	}
}

func boolPtr(v bool) *bool { return &v }
