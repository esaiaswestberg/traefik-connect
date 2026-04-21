package parse

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"example.com/traefik-connect/internal/dockerx"
	"example.com/traefik-connect/internal/model"
)

const (
	LabelOptInPrimary  = "traefik-connect.enable"
	LabelOptInFallback = "traefik.enable"
	LabelBackendURL    = "traefik-connect.backend.url"
	LabelBackendHost   = "traefik-connect.backend.host"
	LabelBackendPort   = "traefik-connect.backend.port"
	LabelBackendScheme = "traefik-connect.backend.scheme"
	labelManaged       = "traefik-connect.managed"
)

type OptInDecision struct {
	Enabled bool
	Reason  string
}

func IsEnabled(labels map[string]string) OptInDecision {
	if v, ok := labels[LabelOptInPrimary]; ok {
		b := parseBool(v, false)
		if b {
			return OptInDecision{Enabled: true, Reason: "traefik-connect.enable=true"}
		}
		return OptInDecision{Enabled: false, Reason: "traefik-connect.enable=false"}
	}
	if v, ok := labels[LabelOptInFallback]; ok {
		b := parseBool(v, false)
		if b {
			return OptInDecision{Enabled: true, Reason: "traefik.enable=true"}
		}
		return OptInDecision{Enabled: false, Reason: "traefik.enable=false"}
	}
	return OptInDecision{Enabled: false, Reason: "no opt-in label"}
}

func BuildContainer(ins dockerx.ContainerInspect, workerID, advertiseAddr string) (model.ContainerSpec, []model.ValidationIssue, error) {
	labels := map[string]string{}
	for k, v := range ins.Config.Labels {
		labels[k] = v
	}
	if parseBool(labels[labelManaged], false) {
		return model.ContainerSpec{}, nil, fmt.Errorf("not exported: managed container")
	}
	decision := IsEnabled(labels)
	if !decision.Enabled {
		return model.ContainerSpec{}, nil, fmt.Errorf("not exported: %s", decision.Reason)
	}

	containerName := strings.TrimPrefix(ins.Name, "/")
	if containerName == "" {
		containerName = ins.Config.Hostname
	}
	if containerName == "" {
		containerName = ins.ID[:12]
	}

	spec := model.ContainerSpec{
		ID:              ins.ID,
		Name:            containerName,
		Image:           ins.Config.Image,
		Labels:          relevantLabels(labels),
		ExportedAt:      time.Now().UTC(),
		ResolutionNotes: []string{decision.Reason},
		Routers:         map[string]model.RouterSpec{},
		Services:        map[string]model.ServiceSpec{},
		Middlewares:     map[string]model.MiddlewareSpec{},
	}

	services := parseServices(labels)
	routers := parseRouters(labels)
	middlewares := parseMiddlewares(labels)

	if len(services) == 0 && len(routers) > 0 {
		services["default"] = serviceStub("default")
		spec.ResolutionNotes = append(spec.ResolutionNotes, "synthesized default service")
	}
	if len(routers) == 0 && len(services) > 0 {
		return model.ContainerSpec{}, nil, fmt.Errorf("no router labels found")
	}
	if len(routers) == 0 {
		return model.ContainerSpec{}, nil, fmt.Errorf("no router labels found")
	}

	for name, svc := range services {
		targetURL, source, port, err := resolveBackendURL(labels, ins, advertiseAddr, svc.Port)
		if err != nil {
			return model.ContainerSpec{}, nil, fmt.Errorf("service %q: %w", name, err)
		}
		svc.Name = name
		svc.BackendURL = targetURL
		svc.BackendSource = source
		svc.Port = port
		spec.Services[name] = svc
	}

	for name, r := range routers {
		r.Name = name
		if strings.TrimSpace(r.Rule) == "" {
			return model.ContainerSpec{}, nil, fmt.Errorf("router %q is missing rule", name)
		}
		if r.Service == "" {
			if len(services) == 1 {
				for svcName := range services {
					r.Service = svcName
				}
			} else if _, ok := services["default"]; ok {
				r.Service = "default"
			} else {
				return model.ContainerSpec{}, nil, fmt.Errorf("router %q does not specify a service and multiple services exist", name)
			}
		}
		if _, ok := spec.Services[r.Service]; !ok {
			return model.ContainerSpec{}, nil, fmt.Errorf("router %q references unknown service %q", name, r.Service)
		}
		for _, mwName := range r.Middlewares {
			if _, ok := middlewares[mwName]; !ok {
				return model.ContainerSpec{}, nil, fmt.Errorf("router %q references unknown middleware %q", name, mwName)
			}
		}
		spec.Routers[name] = r
	}

	for name, mw := range middlewares {
		mw.Name = name
		spec.Middlewares[name] = mw
	}

	return spec, nil, nil
}

func relevantLabels(labels map[string]string) map[string]string {
	out := make(map[string]string)
	for k, v := range labels {
		if strings.HasPrefix(k, "traefik.") || strings.HasPrefix(k, "traefik-connect.") {
			out[k] = v
		}
	}
	return out
}

func parseServices(labels map[string]string) map[string]model.ServiceSpec {
	names := collectNames(labels, "traefik.http.services.")
	out := make(map[string]model.ServiceSpec, len(names))
	for _, name := range names {
		prefix := "traefik.http.services." + name + "."
		svc := model.ServiceSpec{Name: name}
		if v, ok := labels[prefix+"loadbalancer.server.port"]; ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				svc.Port = n
			}
		}
		if v, ok := labels[prefix+"loadbalancer.passhostheader"]; ok {
			b := parseBool(v, true)
			svc.PassHostHeader = &b
		}
		if v, ok := labels[prefix+"loadbalancer.sticky"]; ok {
			b := parseBool(v, false)
			svc.Sticky = &b
		}
		out[name] = svc
	}
	return out
}

func serviceStub(name string) model.ServiceSpec {
	return model.ServiceSpec{Name: name}
}

func parseRouters(labels map[string]string) map[string]model.RouterSpec {
	names := collectNames(labels, "traefik.http.routers.")
	out := make(map[string]model.RouterSpec, len(names))
	for _, name := range names {
		prefix := "traefik.http.routers." + name + "."
		r := model.RouterSpec{Name: name}
		if v, ok := labels[prefix+"rule"]; ok {
			r.Rule = strings.TrimSpace(v)
		}
		if v, ok := labels[prefix+"entrypoints"]; ok {
			r.EntryPoints = csv(v)
		}
		if v, ok := labels[prefix+"service"]; ok {
			r.Service = strings.TrimSpace(v)
		}
		if v, ok := labels[prefix+"middlewares"]; ok {
			r.Middlewares = csv(v)
		}
		if v, ok := labels[prefix+"priority"]; ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				r.Priority = &n
			}
		}
		if v, ok := labels[prefix+"tls"]; ok && parseBool(v, false) {
			r.TLS = &model.TLSSpec{}
		}
		if v, ok := labels[prefix+"tls.certresolver"]; ok {
			if r.TLS == nil {
				r.TLS = &model.TLSSpec{}
			}
			r.TLS.CertResolver = strings.TrimSpace(v)
		}
		out[name] = r
	}
	return out
}

func parseMiddlewares(labels map[string]string) map[string]model.MiddlewareSpec {
	names := collectNames(labels, "traefik.http.middlewares.")
	out := make(map[string]model.MiddlewareSpec, len(names))
	for _, name := range names {
		prefix := "traefik.http.middlewares." + name + "."
		mw := model.MiddlewareSpec{Name: name}
		for k, v := range labels {
			if !strings.HasPrefix(k, prefix) {
				continue
			}
			suffix := strings.TrimPrefix(k, prefix)
			switch {
			case suffix == "redirectscheme.scheme":
				if mw.RedirectScheme == nil {
					mw.RedirectScheme = &model.RedirectScheme{}
				}
				mw.RedirectScheme.Scheme = strings.TrimSpace(v)
			case suffix == "redirectscheme.permanent":
				if mw.RedirectScheme == nil {
					mw.RedirectScheme = &model.RedirectScheme{}
				}
				b := parseBool(v, false)
				mw.RedirectScheme.Permanent = &b
			case suffix == "basicauth.users":
				mw.BasicAuthUsers = csv(v)
			case suffix == "stripprefix.prefixes":
				mw.StripPrefixPrefixes = csv(v)
			case strings.HasPrefix(suffix, "headers."):
				if mw.Headers == nil {
					mw.Headers = &model.HeadersSpec{
						CustomRequestHeaders:  map[string]string{},
						CustomResponseHeaders: map[string]string{},
					}
				}
				parseHeadersLabel(mw.Headers, strings.TrimPrefix(suffix, "headers."), v)
			}
		}
		out[name] = mw
	}
	return out
}

func parseHeadersLabel(h *model.HeadersSpec, key, value string) {
	switch {
	case strings.HasPrefix(key, "customrequestheaders."):
		if h.CustomRequestHeaders == nil {
			h.CustomRequestHeaders = map[string]string{}
		}
		h.CustomRequestHeaders[strings.TrimPrefix(key, "customrequestheaders.")] = value
	case strings.HasPrefix(key, "customresponseheaders."):
		if h.CustomResponseHeaders == nil {
			h.CustomResponseHeaders = map[string]string{}
		}
		h.CustomResponseHeaders[strings.TrimPrefix(key, "customresponseheaders.")] = value
	case key == "sslredirect":
		b := parseBool(value, false)
		h.SSLRedirect = &b
	case key == "stsseconds":
		if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			h.STSSeconds = &n
		}
	case key == "stsincludeddomains":
		b := parseBool(value, false)
		h.STSIncludeSubdomains = &b
	case key == "stspreload":
		b := parseBool(value, false)
		h.STSPreload = &b
	case key == "forcestsheader":
		b := parseBool(value, false)
		h.ForceSTSHeader = &b
	case key == "browserxssfilter":
		b := parseBool(value, false)
		h.BrowserXSSFilter = &b
	case key == "contenttypenosniff":
		b := parseBool(value, false)
		h.ContentTypeNosniff = &b
	case key == "framedeny":
		b := parseBool(value, false)
		h.FrameDeny = &b
	case key == "accesscontrolalloworiginlist":
		h.AccessControlAllowOriginList = csv(value)
	case key == "accesscontrolallowmethods":
		h.AccessControlAllowMethods = csv(value)
	case key == "accesscontrolallowheaders":
		h.AccessControlAllowHeaders = csv(value)
	case key == "accesscontrolexposeheaders":
		h.AccessControlExposeHeaders = csv(value)
	case key == "accesscontrolmaxage":
		h.AccessControlMaxAge = strings.TrimSpace(value)
	case key == "addvaryheader":
		b := parseBool(value, false)
		h.AddVaryHeader = &b
	}
}

func collectNames(labels map[string]string, prefix string) []string {
	seen := map[string]struct{}{}
	for k := range labels {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := strings.TrimPrefix(k, prefix)
		name := strings.SplitN(rest, ".", 2)[0]
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func csv(v string) []string {
	items := strings.Split(v, ",")
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s := strings.TrimSpace(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func parseBool(v string, defaultVal bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "t", "true", "yes", "y", "on":
		return true
	case "0", "f", "false", "no", "n", "off":
		return false
	default:
		return defaultVal
	}
}

func resolveBackendURL(labels map[string]string, ins dockerx.ContainerInspect, advertiseAddr string, servicePort int) (string, string, int, error) {
	if v, ok := labels[LabelBackendURL]; ok && strings.TrimSpace(v) != "" {
		u, err := url.Parse(strings.TrimSpace(v))
		if err != nil {
			return "", "", 0, fmt.Errorf("invalid backend url: %w", err)
		}
		if u.Scheme == "" {
			u.Scheme = "http"
		}
		return u.String(), "override-url", servicePort, nil
	}
	if host, ok := labels[LabelBackendHost]; ok && strings.TrimSpace(host) != "" {
		port := servicePort
		if v, ok := labels[LabelBackendPort]; ok && strings.TrimSpace(v) != "" {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n <= 0 {
				return "", "", 0, fmt.Errorf("invalid backend port: %q", v)
			}
			port = n
		}
		if port <= 0 {
			return "", "", 0, fmt.Errorf("backend host specified without valid port")
		}
		u := url.URL{Scheme: backendScheme(labels, "http"), Host: fmt.Sprintf("%s:%d", strings.TrimSpace(host), port)}
		return u.String(), "override-host-port", port, nil
	}

	port := servicePort
	if port <= 0 {
		var err error
		port, err = inferPort(ins)
		if err != nil {
			return "", "", 0, err
		}
	}
	if strings.EqualFold(ins.HostConfig.NetworkMode, "host") {
		if advertiseAddr == "" {
			return "", "", 0, fmt.Errorf("host network container requires advertise address")
		}
		return fmt.Sprintf("http://%s:%d", advertiseAddr, port), "host-network", port, nil
	}
	if addr := preferredContainerAddr(ins, labels); addr != "" {
		return fmt.Sprintf("http://%s:%d", addr, port), "container-ip", port, nil
	}
	return "", "", 0, fmt.Errorf("no reachable container address for port %d", port)
}

func backendScheme(labels map[string]string, def string) string {
	if v, ok := labels[LabelBackendScheme]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func inferPort(ins dockerx.ContainerInspect) (int, error) {
	if len(ins.Config.ExposedPorts) == 1 {
		for k := range ins.Config.ExposedPorts {
			if n, ok := parseContainerPort(k); ok {
				return n, nil
			}
		}
	}
	ports := map[int]struct{}{}
	for k := range ins.NetworkSettings.Ports {
		if n, ok := parseContainerPort(k); ok {
			ports[n] = struct{}{}
		}
	}
	if len(ports) == 1 {
		for n := range ports {
			return n, nil
		}
	}
	return 0, fmt.Errorf("unable to infer service port; add traefik.http.services.<name>.loadbalancer.server.port")
}

func parseContainerPort(raw string) (int, bool) {
	port := strings.Split(raw, "/")[0]
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func preferredContainerAddr(ins dockerx.ContainerInspect, labels map[string]string) string {
	if ins.NetworkSettings.IPAddress != "" {
		return ins.NetworkSettings.IPAddress
	}
	if len(ins.NetworkSettings.Networks) == 0 {
		return ""
	}
	if preferred := strings.TrimSpace(labels["traefik.docker.network"]); preferred != "" {
		if net, ok := ins.NetworkSettings.Networks[preferred]; ok && net.IPAddress != "" {
			return net.IPAddress
		}
	}
	names := make([]string, 0, len(ins.NetworkSettings.Networks))
	for name := range ins.NetworkSettings.Networks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if ip := strings.TrimSpace(ins.NetworkSettings.Networks[name].IPAddress); ip != "" {
			return ip
		}
	}
	return ""
}
