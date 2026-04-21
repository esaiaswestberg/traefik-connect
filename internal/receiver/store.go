package receiver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"example.com/traefik-connect/internal/dockerx"
	"example.com/traefik-connect/internal/model"
	"example.com/traefik-connect/internal/util"
)

const managedLabelPrefix = "traefik-connect."

type persistedState struct {
	Snapshot  model.Snapshot `json:"snapshot"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type workerRecord struct {
	persistedState
	ManagedContainers []string `json:"managed_containers,omitempty"`
}

type Store struct {
	mu            sync.RWMutex
	stateDir      string
	ttl           time.Duration
	docker        *dockerx.Client
	dockerNetwork string
	stubImage     string
	token         string
	log           *slog.Logger
	workers       map[string]workerRecord
}

func NewStore(stateDir string, ttl time.Duration, docker *dockerx.Client, dockerNetwork, stubImage, token string, log *slog.Logger) *Store {
	return &Store{
		stateDir:      stateDir,
		ttl:           ttl,
		docker:        docker,
		dockerNetwork: dockerNetwork,
		stubImage:     stubImage,
		token:         token,
		log:           log,
		workers:       map[string]workerRecord{},
	}
}

func (s *Store) Load() error {
	if err := os.MkdirAll(s.stateDir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.stateDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.stateDir, entry.Name()))
		if err != nil {
			if s.log != nil {
				s.log.Warn("failed to read persisted worker state", "file", entry.Name(), "error", err)
			}
			continue
		}
		var state workerRecord
		if err := json.Unmarshal(raw, &state); err != nil {
			if s.log != nil {
				s.log.Warn("failed to decode persisted worker state", "file", entry.Name(), "error", err)
			}
			continue
		}
		if !persistedRecordCompatible(state) {
			if s.log != nil {
				s.log.Warn("dropping incompatible persisted worker state", "file", entry.Name(), "worker_id", state.Snapshot.WorkerID)
			}
			_ = os.Remove(filepath.Join(s.stateDir, entry.Name()))
			continue
		}
		workerID := state.Snapshot.WorkerID
		s.workers[workerID] = state
	}
	return s.reconcileAll()
}

func (s *Store) Upsert(snapshot model.Snapshot) (model.Snapshot, []model.ValidationIssue, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	issues, cleaned := validateSnapshot(snapshot)
	if cleaned.WorkerID == "" {
		return model.Snapshot{}, issues, nil, fmt.Errorf("worker id is required")
	}
	if hasSnapshotIssue(issues) {
		return model.Snapshot{}, issues, nil, fmt.Errorf("snapshot is missing required proxy metadata")
	}
	prev, ok := s.workers[cleaned.WorkerID]
	if ok && !cleaned.CapturedAt.After(prev.Snapshot.CapturedAt) {
		return model.Snapshot{}, issues, nil, fmt.Errorf("replayed or stale snapshot")
	}
	if cleaned.Hash == "" {
		cleaned.Hash, _, _ = util.CanonicalHash(snapshotForHash(cleaned))
	}
	record := workerRecord{
		persistedState: persistedState{
			Snapshot:  cleaned,
			UpdatedAt: time.Now().UTC(),
		},
		ManagedContainers: prev.ManagedContainers,
	}
	s.workers[cleaned.WorkerID] = record
	if err := s.persistLocked(record); err != nil {
		return model.Snapshot{}, issues, nil, err
	}
	managed, reconcileIssues, err := s.reconcileWorker(record)
	issues = append(issues, reconcileIssues...)
	record.ManagedContainers = managed
	record.UpdatedAt = time.Now().UTC()
	s.workers[cleaned.WorkerID] = record
	if persistErr := s.persistLocked(record); persistErr != nil {
		return model.Snapshot{}, issues, managed, persistErr
	}
	if err != nil && len(managed) == 0 {
		return model.Snapshot{}, issues, managed, err
	}
	return cleaned, issues, managed, nil
}

func (s *Store) RemoveExpired(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ttl <= 0 {
		return nil
	}
	for workerID, record := range s.workers {
		if now.Sub(record.UpdatedAt) <= s.ttl {
			continue
		}
		delete(s.workers, workerID)
		_ = s.removeManagedContainers(record.Snapshot.WorkerID, record.ManagedContainers)
		_ = os.Remove(filepath.Join(s.stateDir, workerID+".json"))
	}
	return nil
}

func (s *Store) Statuses() []Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Status, 0, len(s.workers))
	for _, record := range s.workers {
		out = append(out, Status{
			WorkerID:          record.Snapshot.WorkerID,
			CapturedAt:        record.Snapshot.CapturedAt,
			UpdatedAt:         record.UpdatedAt,
			Hash:              record.Snapshot.Hash,
			ContainerCount:    len(record.Snapshot.Containers),
			ManagedContainers: append([]string(nil), record.ManagedContainers...),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkerID < out[j].WorkerID })
	return out
}

func (s *Store) reconcileAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for workerID, record := range s.workers {
		managed, issues, err := s.reconcileWorker(record)
		if len(issues) > 0 && s.log != nil {
			for _, issue := range issues {
				s.log.Warn("stub reconciliation issue", "worker_id", workerID, "container_id", issue.ContainerID, "field", issue.Field, "message", issue.Message)
			}
		}
		if err != nil && len(managed) == 0 {
			if s.log != nil {
				s.log.Warn("failed to reconcile worker snapshot", "worker_id", workerID, "error", err)
			}
			continue
		}
		record.ManagedContainers = managed
		record.UpdatedAt = time.Now().UTC()
		s.workers[workerID] = record
		_ = s.persistLocked(record)
	}
	return nil
}

func (s *Store) reconcileWorker(record workerRecord) ([]string, []model.ValidationIssue, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	desired, issues, err := buildDesiredStubs(record.Snapshot, s.dockerNetwork, s.stubImage, s.token)
	if err != nil {
		return nil, issues, err
	}
	desiredByName := make(map[string]stubSpec, len(desired))
	for _, spec := range desired {
		desiredByName[spec.Name] = spec
	}
	existing, err := s.listManagedContainers(ctx, record.Snapshot.WorkerID)
	if err != nil {
		return nil, issues, err
	}
	managed := make([]string, 0, len(desiredByName))
	for name, spec := range desiredByName {
		if current, ok := existing[name]; ok {
			if current.DesiredHash == spec.DesiredHash && current.Running {
				managed = append(managed, name)
				continue
			}
			_ = s.docker.RemoveContainer(ctx, current.ID, true)
		}
		id, err := s.createStubContainer(ctx, spec)
		if err != nil {
			issues = append(issues, model.ValidationIssue{
				WorkerID:    record.Snapshot.WorkerID,
				ContainerID: spec.ContainerID,
				Container:   spec.ContainerName,
				Scope:       "stub",
				Field:       "create",
				Message:     err.Error(),
			})
			continue
		}
		if id != "" {
			managed = append(managed, name)
		}
	}
	for name, current := range existing {
		if _, ok := desiredByName[name]; ok {
			continue
		}
		_ = s.docker.RemoveContainer(ctx, current.ID, true)
	}
	if len(managed) == 0 && len(desiredByName) > 0 {
		return managed, issues, fmt.Errorf("failed to create any managed stub containers")
	}
	return managed, issues, nil
}

func (s *Store) listManagedContainers(ctx context.Context, workerID string) (map[string]managedContainer, error) {
	summaries, err := s.docker.ListContainers(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]managedContainer)
	for _, summary := range summaries {
		ins, err := s.docker.InspectContainer(ctx, summary.ID)
		if err != nil {
			continue
		}
		labels := ins.Config.Labels
		if labels == nil || labels[managedLabelPrefix+"managed"] != "true" {
			continue
		}
		if labels[managedLabelPrefix+"worker-id"] != workerID {
			continue
		}
		name := strings.TrimPrefix(ins.Name, "/")
		out[name] = managedContainer{
			ID:          ins.ID,
			Name:        name,
			DesiredHash: labels[managedLabelPrefix+"desired-hash"],
			Running:     ins.State.Running,
		}
	}
	return out, nil
}

func (s *Store) createStubContainer(ctx context.Context, spec stubSpec) (string, error) {
	req := dockerx.ContainerCreateRequest{
		Image:  spec.Image,
		Env:    spec.Env,
		Cmd:    []string{"stub"},
		Labels: spec.Labels,
		ExposedPorts: map[string]struct{}{
			"8080/tcp": {},
		},
		HostConfig: &dockerx.ContainerHostConfig{AutoRemove: false},
	}
	resp, err := s.docker.CreateContainer(ctx, spec.Name, req)
	if err != nil {
		return "", err
	}
	if s.dockerNetwork != "" {
		if err := s.docker.ConnectNetwork(ctx, s.dockerNetwork, resp.ID); err != nil {
			_ = s.docker.RemoveContainer(ctx, resp.ID, true)
			return "", err
		}
	}
	if err := s.docker.StartContainer(ctx, resp.ID); err != nil {
		_ = s.docker.RemoveContainer(ctx, resp.ID, true)
		return "", err
	}
	return resp.ID, nil
}

func (s *Store) removeManagedContainers(workerID string, names []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	existing, err := s.listManagedContainers(ctx, workerID)
	if err != nil {
		return err
	}
	for _, name := range names {
		if rec, ok := existing[name]; ok {
			_ = s.docker.RemoveContainer(ctx, rec.ID, true)
		}
	}
	return nil
}

func (s *Store) persistLocked(record workerRecord) error {
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return util.AtomicWriteFile(filepath.Join(s.stateDir, record.Snapshot.WorkerID+".json"), body, 0o644)
}

type Status struct {
	WorkerID          string    `json:"worker_id"`
	CapturedAt        time.Time `json:"captured_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	Hash              string    `json:"hash"`
	ContainerCount    int       `json:"container_count"`
	ManagedContainers []string  `json:"managed_containers,omitempty"`
}

type managedContainer struct {
	ID          string
	Name        string
	DesiredHash string
	Running     bool
}

type stubSpec struct {
	Name          string
	Image         string
	ContainerID   string
	ContainerName string
	ServiceName   string
	DesiredHash   string
	Labels        map[string]string
	Env           []string
}

func buildDesiredStubs(snapshot model.Snapshot, dockerNetwork, stubImage, token string) ([]stubSpec, []model.ValidationIssue, error) {
	if snapshot.AdvertiseAddr == "" {
		return nil, []model.ValidationIssue{{
			WorkerID: snapshot.WorkerID,
			Scope:    "snapshot",
			Field:    "advertise_addr",
			Message:  "missing advertise address",
		}}, fmt.Errorf("missing advertise address")
	}
	if snapshot.ProxyPort <= 0 {
		return nil, []model.ValidationIssue{{
			WorkerID: snapshot.WorkerID,
			Scope:    "snapshot",
			Field:    "proxy_port",
			Message:  "missing proxy port",
		}}, fmt.Errorf("missing proxy port")
	}
	var specs []stubSpec
	for _, c := range snapshot.Containers {
		routerNamesByService := map[string][]string{}
		for _, routerName := range sortedRouterNames(c.Routers) {
			r := c.Routers[routerName]
			if r.Service == "" {
				continue
			}
			routerNamesByService[r.Service] = append(routerNamesByService[r.Service], routerName)
		}
		for _, serviceName := range sortedServiceNames(c.Services) {
			if len(routerNamesByService[serviceName]) == 0 {
				continue
			}
			svc := c.Services[serviceName]
			spec := stubSpec{
				Name:          util.SanitizeName("tc-" + util.ShortHash(snapshot.WorkerID, c.ID, serviceName)),
				Image:         stubImage,
				ContainerID:   c.ID,
				ContainerName: c.Name,
				ServiceName:   serviceName,
				Labels:        map[string]string{},
				Env: []string{
					"STUB_TARGET_URL=" + fmt.Sprintf("http://%s:%d", snapshot.AdvertiseAddr, snapshot.ProxyPort),
					"STUB_TOKEN=" + token,
					"STUB_CONTAINER_ID=" + c.ID,
					"STUB_SERVICE_NAME=" + serviceName,
					"STUB_LISTEN_ADDR=:8080",
				},
			}
			spec.Labels[managedLabelPrefix+"managed"] = "true"
			spec.Labels[managedLabelPrefix+"worker-id"] = snapshot.WorkerID
			spec.Labels[managedLabelPrefix+"container-id"] = c.ID
			spec.Labels[managedLabelPrefix+"container-name"] = c.Name
			spec.Labels[managedLabelPrefix+"service-name"] = serviceName
			spec.Labels[managedLabelPrefix+"desired-hash"] = ""
			spec.Labels["traefik.enable"] = "true"
			if dockerNetwork != "" {
				spec.Labels["traefik.docker.network"] = dockerNetwork
			}
			spec.Labels["traefik.http.services."+serviceName+".loadbalancer.server.port"] = "8080"
			if svc.PassHostHeader != nil {
				spec.Labels["traefik.http.services."+serviceName+".loadbalancer.passhostheader"] = strconv.FormatBool(*svc.PassHostHeader)
			}
			if svc.Sticky != nil {
				spec.Labels["traefik.http.services."+serviceName+".loadbalancer.sticky"] = strconv.FormatBool(*svc.Sticky)
			}
			for _, routerName := range routerNamesByService[serviceName] {
				r := c.Routers[routerName]
				spec.Labels["traefik.http.routers."+routerName+".rule"] = r.Rule
				if len(r.EntryPoints) > 0 {
					spec.Labels["traefik.http.routers."+routerName+".entrypoints"] = strings.Join(r.EntryPoints, ",")
				}
				if r.TLS != nil {
					spec.Labels["traefik.http.routers."+routerName+".tls"] = "true"
					if r.TLS.CertResolver != "" {
						spec.Labels["traefik.http.routers."+routerName+".tls.certresolver"] = r.TLS.CertResolver
					}
				}
				if len(r.Middlewares) > 0 {
					spec.Labels["traefik.http.routers."+routerName+".middlewares"] = strings.Join(r.Middlewares, ",")
				}
				spec.Labels["traefik.http.routers."+routerName+".service"] = serviceName
				if r.Priority != nil {
					spec.Labels["traefik.http.routers."+routerName+".priority"] = strconv.Itoa(*r.Priority)
				}
				for _, mwName := range r.Middlewares {
					if mw, ok := c.Middlewares[mwName]; ok {
						for k, v := range middlewareLabels(mwName, mw) {
							spec.Labels[k] = v
						}
					}
				}
			}
			hash, _, err := util.CanonicalHash(map[string]any{
				"worker_id": snapshot.WorkerID,
				"container": c.ID,
				"service":   serviceName,
				"target":    spec.Env[0],
				"labels":    spec.Labels,
				"image":     spec.Image,
			})
			if err != nil {
				return nil, nil, err
			}
			spec.DesiredHash = hash
			spec.Labels[managedLabelPrefix+"desired-hash"] = hash
			specs = append(specs, spec)
		}
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs, nil, nil
}

func middlewareLabels(name string, mw model.MiddlewareSpec) map[string]string {
	out := map[string]string{}
	if mw.RedirectScheme != nil {
		if mw.RedirectScheme.Scheme != "" {
			out["traefik.http.middlewares."+name+".redirectscheme.scheme"] = mw.RedirectScheme.Scheme
		}
		if mw.RedirectScheme.Permanent != nil {
			out["traefik.http.middlewares."+name+".redirectscheme.permanent"] = strconv.FormatBool(*mw.RedirectScheme.Permanent)
		}
	}
	if mw.Headers != nil {
		for k, v := range mw.Headers.CustomRequestHeaders {
			out["traefik.http.middlewares."+name+".headers.customrequestheaders."+k] = v
		}
		for k, v := range mw.Headers.CustomResponseHeaders {
			out["traefik.http.middlewares."+name+".headers.customresponseheaders."+k] = v
		}
		if mw.Headers.SSLRedirect != nil {
			out["traefik.http.middlewares."+name+".headers.sslredirect"] = strconv.FormatBool(*mw.Headers.SSLRedirect)
		}
		if mw.Headers.STSSeconds != nil {
			out["traefik.http.middlewares."+name+".headers.stsseconds"] = strconv.Itoa(*mw.Headers.STSSeconds)
		}
		if mw.Headers.STSIncludeSubdomains != nil {
			out["traefik.http.middlewares."+name+".headers.stsincludeddomains"] = strconv.FormatBool(*mw.Headers.STSIncludeSubdomains)
		}
		if mw.Headers.STSPreload != nil {
			out["traefik.http.middlewares."+name+".headers.stspreload"] = strconv.FormatBool(*mw.Headers.STSPreload)
		}
		if mw.Headers.ForceSTSHeader != nil {
			out["traefik.http.middlewares."+name+".headers.forcestsheader"] = strconv.FormatBool(*mw.Headers.ForceSTSHeader)
		}
		if mw.Headers.BrowserXSSFilter != nil {
			out["traefik.http.middlewares."+name+".headers.browserxssfilter"] = strconv.FormatBool(*mw.Headers.BrowserXSSFilter)
		}
		if mw.Headers.ContentTypeNosniff != nil {
			out["traefik.http.middlewares."+name+".headers.contenttypenosniff"] = strconv.FormatBool(*mw.Headers.ContentTypeNosniff)
		}
		if mw.Headers.FrameDeny != nil {
			out["traefik.http.middlewares."+name+".headers.framedeny"] = strconv.FormatBool(*mw.Headers.FrameDeny)
		}
		if len(mw.Headers.AccessControlAllowOriginList) > 0 {
			out["traefik.http.middlewares."+name+".headers.accesscontrolalloworiginlist"] = strings.Join(mw.Headers.AccessControlAllowOriginList, ",")
		}
		if len(mw.Headers.AccessControlAllowMethods) > 0 {
			out["traefik.http.middlewares."+name+".headers.accesscontrolallowmethods"] = strings.Join(mw.Headers.AccessControlAllowMethods, ",")
		}
		if len(mw.Headers.AccessControlAllowHeaders) > 0 {
			out["traefik.http.middlewares."+name+".headers.accesscontrolallowheaders"] = strings.Join(mw.Headers.AccessControlAllowHeaders, ",")
		}
		if len(mw.Headers.AccessControlExposeHeaders) > 0 {
			out["traefik.http.middlewares."+name+".headers.accesscontrolexposeheaders"] = strings.Join(mw.Headers.AccessControlExposeHeaders, ",")
		}
		if mw.Headers.AccessControlMaxAge != "" {
			out["traefik.http.middlewares."+name+".headers.accesscontrolmaxage"] = mw.Headers.AccessControlMaxAge
		}
		if mw.Headers.AddVaryHeader != nil {
			out["traefik.http.middlewares."+name+".headers.addvaryheader"] = strconv.FormatBool(*mw.Headers.AddVaryHeader)
		}
	}
	if len(mw.BasicAuthUsers) > 0 {
		out["traefik.http.middlewares."+name+".basicauth.users"] = strings.Join(mw.BasicAuthUsers, ",")
	}
	if len(mw.StripPrefixPrefixes) > 0 {
		out["traefik.http.middlewares."+name+".stripprefix.prefixes"] = strings.Join(mw.StripPrefixPrefixes, ",")
	}
	return out
}

func sortedRouterNames(m map[string]model.RouterSpec) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedServiceNames(m map[string]model.ServiceSpec) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func validateSnapshot(snapshot model.Snapshot) ([]model.ValidationIssue, model.Snapshot) {
	cleaned := snapshot
	cleaned.Containers = nil
	issues := make([]model.ValidationIssue, 0)
	if snapshot.WorkerID == "" {
		issues = append(issues, model.ValidationIssue{Scope: "snapshot", Field: "worker_id", Message: "missing worker id"})
		return issues, cleaned
	}
	if snapshot.AdvertiseAddr == "" {
		issues = append(issues, model.ValidationIssue{WorkerID: snapshot.WorkerID, Scope: "snapshot", Field: "advertise_addr", Message: "missing advertise address"})
	}
	if snapshot.ProxyPort <= 0 {
		issues = append(issues, model.ValidationIssue{WorkerID: snapshot.WorkerID, Scope: "snapshot", Field: "proxy_port", Message: "missing proxy port"})
	}
	seen := map[string]struct{}{}
	for _, c := range snapshot.Containers {
		if c.ID == "" {
			issues = append(issues, model.ValidationIssue{WorkerID: snapshot.WorkerID, Container: c.Name, ContainerID: c.ID, Scope: "container", Field: "id", Message: "missing container id"})
			continue
		}
		if _, ok := seen[c.ID]; ok {
			issues = append(issues, model.ValidationIssue{WorkerID: snapshot.WorkerID, Container: c.Name, ContainerID: c.ID, Scope: "container", Field: "id", Message: "duplicate container id"})
			continue
		}
		seen[c.ID] = struct{}{}
		if c.Name == "" {
			issues = append(issues, model.ValidationIssue{WorkerID: snapshot.WorkerID, ContainerID: c.ID, Scope: "container", Field: "name", Message: "missing container name"})
			continue
		}
		if len(c.Routers) == 0 || len(c.Services) == 0 {
			issues = append(issues, model.ValidationIssue{WorkerID: snapshot.WorkerID, ContainerID: c.ID, Container: c.Name, Scope: "container", Field: "routes", Message: "missing routers or services"})
			continue
		}
		cleaned.Containers = append(cleaned.Containers, c)
	}
	return issues, cleaned
}

func persistedRecordCompatible(record workerRecord) bool {
	if record.Snapshot.WorkerID == "" {
		return false
	}
	if record.Snapshot.AdvertiseAddr == "" {
		return false
	}
	if record.Snapshot.ProxyPort <= 0 {
		return false
	}
	return true
}

func hasSnapshotIssue(issues []model.ValidationIssue) bool {
	for _, issue := range issues {
		if issue.Scope == "snapshot" {
			return true
		}
	}
	return false
}

func snapshotForHash(snapshot model.Snapshot) any {
	cp := snapshot
	cp.Hash = ""
	return cp
}
