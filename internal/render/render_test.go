package render

import (
	"strings"
	"testing"
	"time"

	"example.com/traefik-connect/internal/model"
)

func TestRenderSnapshot(t *testing.T) {
	snapshot := model.Snapshot{
		WorkerID:   "worker-a",
		CapturedAt: time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		Version:    "v1",
		Containers: []model.ContainerSpec{
			{
				ID:   "abcdef1234567890",
				Name: "web",
				Routers: map[string]model.RouterSpec{
					"web": {
						Rule:        "Host(`web.example.test`)",
						EntryPoints: []string{"web", "websecure"},
						Service:     "websvc",
						Priority:    intPtr(10),
						TLS:         &model.TLSSpec{CertResolver: "letsencrypt"},
					},
				},
				Services: map[string]model.ServiceSpec{
					"websvc": {
						BackendURL:     "http://192.168.1.10:18080",
						PassHostHeader: boolPtr(true),
						Sticky:         boolPtr(true),
					},
				},
				Middlewares: map[string]model.MiddlewareSpec{
					"secure": {
						RedirectScheme: &model.RedirectScheme{Scheme: "https", Permanent: boolPtr(true)},
					},
				},
			},
		},
	}

	out, err := RenderSnapshot(snapshot)
	if err != nil {
		t.Fatalf("RenderSnapshot() error = %v", err)
	}
	got := string(out)
	for _, want := range []string{
		"http:",
		"routers:",
		"services:",
		"middlewares:",
		"worker-a-web-abcdef123456-web:",
		"rule: \"Host(`web.example.test`)\"",
		"entryPoints:",
		"- \"web\"",
		"- \"websecure\"",
		"tls:",
		"certResolver: \"letsencrypt\"",
		"loadBalancer:",
		"servers:",
		"url: \"http://192.168.1.10:18080\"",
		"passHostHeader: true",
		"sticky: {}",
		"redirectScheme:",
		"scheme: \"https\"",
		"permanent: true",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered config missing %q\n%s", want, got)
		}
	}
}

func boolPtr(v bool) *bool { return &v }
func intPtr(v int) *int    { return &v }
