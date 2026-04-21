package parse

import (
	"testing"

	"example.com/traefik-connect/internal/dockerx"
)

func TestBuildContainerResolvesPublishedPort(t *testing.T) {
	ins := dockerx.ContainerInspect{}
	ins.ID = "abcdef1234567890"
	ins.Name = "/web"
	ins.Config.Image = "example/web:latest"
	ins.Config.Labels = map[string]string{
		"traefik-sync.enable":                                      "true",
		"traefik.http.routers.web.rule":                            "Host(`web.example.test`)",
		"traefik.http.routers.web.entrypoints":                     "web",
		"traefik.http.routers.web.tls":                             "true",
		"traefik.http.routers.web.tls.certresolver":                "letsencrypt",
		"traefik.http.routers.web.middlewares":                     "secure",
		"traefik.http.routers.web.service":                         "websvc",
		"traefik.http.services.websvc.loadbalancer.server.port":    "8080",
		"traefik.http.services.websvc.loadbalancer.passhostheader": "true",
		"traefik.http.middlewares.secure.redirectscheme.scheme":    "https",
		"traefik.http.middlewares.secure.redirectscheme.permanent": "true",
	}
	ins.NetworkSettings.Ports = map[string][]dockerx.PortBinding{
		"8080/tcp": {{HostIp: "0.0.0.0", HostPort: "18080"}},
	}

	spec, _, err := BuildContainer(ins, "worker-a", "192.168.1.10")
	if err != nil {
		t.Fatalf("BuildContainer() error = %v", err)
	}
	if got := spec.Services["websvc"].BackendURL; got != "http://192.168.1.10:18080" {
		t.Fatalf("BackendURL = %q", got)
	}
	if spec.Routers["web"].Service != "websvc" {
		t.Fatalf("router service = %q", spec.Routers["web"].Service)
	}
	if spec.Routers["web"].TLS == nil || spec.Routers["web"].TLS.CertResolver != "letsencrypt" {
		t.Fatalf("expected tls certresolver, got %#v", spec.Routers["web"].TLS)
	}
	if spec.Middlewares["secure"].RedirectScheme == nil || spec.Middlewares["secure"].RedirectScheme.Scheme != "https" {
		t.Fatalf("expected redirect scheme middleware, got %#v", spec.Middlewares["secure"].RedirectScheme)
	}
}

func TestBuildContainerRejectsDisabledOptIn(t *testing.T) {
	ins := dockerx.ContainerInspect{}
	ins.ID = "abcdef1234567890"
	ins.Name = "/web"
	ins.Config.Labels = map[string]string{
		"traefik-sync.enable": "false",
		"traefik.enable":      "true",
	}
	_, _, err := BuildContainer(ins, "worker-a", "192.168.1.10")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildContainerRejectsLocalhostBinding(t *testing.T) {
	ins := dockerx.ContainerInspect{}
	ins.ID = "abcdef1234567890"
	ins.Name = "/web"
	ins.Config.Labels = map[string]string{
		"traefik-sync.enable":                                   "true",
		"traefik.http.routers.web.rule":                         "Host(`web.example.test`)",
		"traefik.http.routers.web.service":                      "websvc",
		"traefik.http.services.websvc.loadbalancer.server.port": "8080",
	}
	ins.NetworkSettings.Ports = map[string][]dockerx.PortBinding{
		"8080/tcp": {{HostIp: "127.0.0.1", HostPort: "18080"}},
	}
	_, _, err := BuildContainer(ins, "worker-a", "192.168.1.10")
	if err == nil {
		t.Fatal("expected error")
	}
}
