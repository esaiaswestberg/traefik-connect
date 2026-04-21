package render

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"example.com/traefik-connect/internal/model"
	"example.com/traefik-connect/internal/util"
)

type block struct {
	name string
	body string
}

func RenderSnapshot(snapshot model.Snapshot) ([]byte, error) {
	var buf bytes.Buffer
	containers := append([]model.ContainerSpec(nil), snapshot.Containers...)
	sort.SliceStable(containers, func(i, j int) bool {
		if containers[i].Name == containers[j].Name {
			return containers[i].ID < containers[j].ID
		}
		return containers[i].Name < containers[j].Name
	})

	var routerBlocks, serviceBlocks, middlewareBlocks []block
	for _, c := range containers {
		r, s, m, err := renderContainer(snapshot.WorkerID, c)
		if err != nil {
			return nil, err
		}
		routerBlocks = append(routerBlocks, r...)
		serviceBlocks = append(serviceBlocks, s...)
		middlewareBlocks = append(middlewareBlocks, m...)
	}

	if len(routerBlocks) == 0 && len(serviceBlocks) == 0 && len(middlewareBlocks) == 0 {
		return []byte{}, nil
	}

	buf.WriteString("http:\n")
	if len(routerBlocks) > 0 {
		writeSection(&buf, 1, "routers", routerBlocks)
	}
	if len(serviceBlocks) > 0 {
		writeSection(&buf, 1, "services", serviceBlocks)
	}
	if len(middlewareBlocks) > 0 {
		writeSection(&buf, 1, "middlewares", middlewareBlocks)
	}
	return buf.Bytes(), nil
}

func renderContainer(workerID string, c model.ContainerSpec) ([]block, []block, []block, error) {
	routerMap := map[string]string{}
	serviceMap := map[string]string{}
	mwMap := map[string]string{}

	for _, name := range sortedKeys(c.Routers) {
		routerMap[name] = qualifiedName(workerID, c, name)
	}
	for _, name := range sortedKeys(c.Services) {
		serviceMap[name] = qualifiedName(workerID, c, name)
	}
	for _, name := range sortedKeys(c.Middlewares) {
		mwMap[name] = qualifiedName(workerID, c, name)
	}

	var routers, services, middlewares []block
	for _, local := range sortedKeys(c.Routers) {
		b, err := renderRouter(routerMap[local], c.Routers[local], serviceMap, mwMap)
		if err != nil {
			return nil, nil, nil, err
		}
		routers = append(routers, b)
	}
	for _, local := range sortedKeys(c.Services) {
		b, err := renderService(serviceMap[local], c.Services[local])
		if err != nil {
			return nil, nil, nil, err
		}
		services = append(services, b)
	}
	for _, local := range sortedKeys(c.Middlewares) {
		b, err := renderMiddleware(mwMap[local], c.Middlewares[local])
		if err != nil {
			return nil, nil, nil, err
		}
		middlewares = append(middlewares, b)
	}
	return routers, services, middlewares, nil
}

func renderRouter(name string, r model.RouterSpec, serviceMap, mwMap map[string]string) (block, error) {
	var buf strings.Builder
	writeIndent(&buf, 2)
	buf.WriteString(name)
	buf.WriteString(":\n")
	if r.Rule != "" {
		writeKeyValue(&buf, 3, "rule", quote(r.Rule))
	}
	if len(r.EntryPoints) > 0 {
		writeStringList(&buf, 3, "entryPoints", r.EntryPoints)
	}
	if r.TLS != nil {
		writeIndent(&buf, 3)
		buf.WriteString("tls")
		if r.TLS.CertResolver == "" {
			buf.WriteString(": {}\n")
		} else {
			buf.WriteString(":\n")
			writeKeyValue(&buf, 4, "certResolver", quote(r.TLS.CertResolver))
		}
	}
	if len(r.Middlewares) > 0 {
		var resolved []string
		for _, local := range r.Middlewares {
			if g, ok := mwMap[local]; ok {
				resolved = append(resolved, g)
			}
		}
		if len(resolved) > 0 {
			writeStringList(&buf, 3, "middlewares", resolved)
		}
	}
	if r.Service != "" {
		if g, ok := serviceMap[r.Service]; ok {
			writeKeyValue(&buf, 3, "service", quote(g))
		}
	}
	if r.Priority != nil {
		writeKeyValue(&buf, 3, "priority", fmt.Sprintf("%d", *r.Priority))
	}
	return block{name: name, body: buf.String()}, nil
}

func renderService(name string, s model.ServiceSpec) (block, error) {
	var buf strings.Builder
	writeIndent(&buf, 2)
	buf.WriteString(name)
	buf.WriteString(":\n")
	writeIndent(&buf, 3)
	buf.WriteString("loadBalancer:\n")
	writeIndent(&buf, 4)
	buf.WriteString("servers:\n")
	writeIndent(&buf, 5)
	buf.WriteString("- url: ")
	buf.WriteString(quote(s.BackendURL))
	buf.WriteByte('\n')
	if s.PassHostHeader != nil {
		writeKeyValue(&buf, 4, "passHostHeader", fmt.Sprintf("%t", *s.PassHostHeader))
	}
	if s.Sticky != nil && *s.Sticky {
		writeIndent(&buf, 4)
		buf.WriteString("sticky: {}\n")
	}
	return block{name: name, body: buf.String()}, nil
}

func renderMiddleware(name string, m model.MiddlewareSpec) (block, error) {
	var buf strings.Builder
	writeIndent(&buf, 2)
	buf.WriteString(name)
	buf.WriteString(":\n")
	if m.RedirectScheme != nil {
		writeIndent(&buf, 3)
		buf.WriteString("redirectScheme:\n")
		if m.RedirectScheme.Scheme != "" {
			writeKeyValue(&buf, 4, "scheme", quote(m.RedirectScheme.Scheme))
		}
		if m.RedirectScheme.Permanent != nil {
			writeKeyValue(&buf, 4, "permanent", fmt.Sprintf("%t", *m.RedirectScheme.Permanent))
		}
	}
	if m.Headers != nil {
		writeIndent(&buf, 3)
		buf.WriteString("headers:\n")
		if len(m.Headers.CustomRequestHeaders) > 0 {
			writeIndent(&buf, 4)
			buf.WriteString("customRequestHeaders:\n")
			writeStringMap(&buf, 5, m.Headers.CustomRequestHeaders)
		}
		if len(m.Headers.CustomResponseHeaders) > 0 {
			writeIndent(&buf, 4)
			buf.WriteString("customResponseHeaders:\n")
			writeStringMap(&buf, 5, m.Headers.CustomResponseHeaders)
		}
		if m.Headers.SSLRedirect != nil {
			writeKeyValue(&buf, 4, "sslRedirect", fmt.Sprintf("%t", *m.Headers.SSLRedirect))
		}
		if m.Headers.STSSeconds != nil {
			writeKeyValue(&buf, 4, "stsSeconds", fmt.Sprintf("%d", *m.Headers.STSSeconds))
		}
		if m.Headers.STSIncludeSubdomains != nil {
			writeKeyValue(&buf, 4, "stsIncludeSubdomains", fmt.Sprintf("%t", *m.Headers.STSIncludeSubdomains))
		}
		if m.Headers.STSPreload != nil {
			writeKeyValue(&buf, 4, "stsPreload", fmt.Sprintf("%t", *m.Headers.STSPreload))
		}
		if m.Headers.ForceSTSHeader != nil {
			writeKeyValue(&buf, 4, "forceSTSHeader", fmt.Sprintf("%t", *m.Headers.ForceSTSHeader))
		}
		if m.Headers.BrowserXSSFilter != nil {
			writeKeyValue(&buf, 4, "browserXSSFilter", fmt.Sprintf("%t", *m.Headers.BrowserXSSFilter))
		}
		if m.Headers.ContentTypeNosniff != nil {
			writeKeyValue(&buf, 4, "contentTypeNosniff", fmt.Sprintf("%t", *m.Headers.ContentTypeNosniff))
		}
		if m.Headers.FrameDeny != nil {
			writeKeyValue(&buf, 4, "frameDeny", fmt.Sprintf("%t", *m.Headers.FrameDeny))
		}
		if len(m.Headers.AccessControlAllowOriginList) > 0 {
			writeStringList(&buf, 4, "accessControlAllowOriginList", m.Headers.AccessControlAllowOriginList)
		}
		if len(m.Headers.AccessControlAllowMethods) > 0 {
			writeStringList(&buf, 4, "accessControlAllowMethods", m.Headers.AccessControlAllowMethods)
		}
		if len(m.Headers.AccessControlAllowHeaders) > 0 {
			writeStringList(&buf, 4, "accessControlAllowHeaders", m.Headers.AccessControlAllowHeaders)
		}
		if len(m.Headers.AccessControlExposeHeaders) > 0 {
			writeStringList(&buf, 4, "accessControlExposeHeaders", m.Headers.AccessControlExposeHeaders)
		}
		if m.Headers.AccessControlMaxAge != "" {
			writeKeyValue(&buf, 4, "accessControlMaxAge", quote(m.Headers.AccessControlMaxAge))
		}
		if m.Headers.AddVaryHeader != nil {
			writeKeyValue(&buf, 4, "addVaryHeader", fmt.Sprintf("%t", *m.Headers.AddVaryHeader))
		}
	}
	if len(m.BasicAuthUsers) > 0 {
		writeIndent(&buf, 3)
		buf.WriteString("basicAuth:\n")
		writeStringList(&buf, 4, "users", m.BasicAuthUsers)
	}
	if len(m.StripPrefixPrefixes) > 0 {
		writeIndent(&buf, 3)
		buf.WriteString("stripPrefix:\n")
		writeStringList(&buf, 4, "prefixes", m.StripPrefixPrefixes)
	}
	return block{name: name, body: buf.String()}, nil
}

func writeSection(buf *bytes.Buffer, indent int, name string, blocks []block) {
	writeIndent(buf, indent)
	buf.WriteString(name)
	buf.WriteString(":\n")
	sort.SliceStable(blocks, func(i, j int) bool { return blocks[i].name < blocks[j].name })
	for _, b := range blocks {
		buf.WriteString(b.body)
	}
}

func writeKeyValue(buf *strings.Builder, indent int, key, value string) {
	writeIndent(buf, indent)
	buf.WriteString(key)
	buf.WriteString(": ")
	buf.WriteString(value)
	buf.WriteByte('\n')
}

func writeStringList(buf *strings.Builder, indent int, key string, values []string) {
	writeIndent(buf, indent)
	buf.WriteString(key)
	buf.WriteString(":\n")
	items := append([]string(nil), values...)
	sort.Strings(items)
	for _, value := range items {
		writeIndent(buf, indent+1)
		buf.WriteString("- ")
		buf.WriteString(quote(value))
		buf.WriteByte('\n')
	}
}

func writeStringMap(buf *strings.Builder, indent int, values map[string]string) {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		writeIndent(buf, indent)
		buf.WriteString(key)
		buf.WriteString(": ")
		buf.WriteString(quote(values[key]))
		buf.WriteByte('\n')
	}
}

func writeIndent(buf interface{ WriteString(string) (int, error) }, indent int) {
	for i := 0; i < indent; i++ {
		_, _ = buf.WriteString("  ")
	}
}

func quote(s string) string {
	return strconv.Quote(s)
}

func qualifiedName(workerID string, c model.ContainerSpec, local string) string {
	shortID := c.ID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	return util.SanitizeName(workerID) + "-" + util.SanitizeName(c.Name) + "-" + util.SanitizeName(shortID) + "-" + util.SanitizeName(local)
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
