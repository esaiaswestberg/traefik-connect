package model

import "time"

type Snapshot struct {
	WorkerID      string          `json:"worker_id" yaml:"worker_id"`
	AdvertiseAddr string          `json:"advertise_addr,omitempty" yaml:"advertise_addr,omitempty"`
	CapturedAt    time.Time       `json:"captured_at" yaml:"captured_at"`
	Version       string          `json:"version" yaml:"version"`
	Hash          string          `json:"hash" yaml:"hash"`
	Containers    []ContainerSpec `json:"containers" yaml:"containers"`
	DecisionNotes []string        `json:"decision_notes,omitempty" yaml:"decision_notes,omitempty"`
}

type ContainerSpec struct {
	ID              string                    `json:"id" yaml:"id"`
	Name            string                    `json:"name" yaml:"name"`
	Image           string                    `json:"image,omitempty" yaml:"image,omitempty"`
	Labels          map[string]string         `json:"labels" yaml:"labels"`
	ExportedAt      time.Time                 `json:"exported_at" yaml:"exported_at"`
	ResolutionNotes []string                  `json:"resolution_notes,omitempty" yaml:"resolution_notes,omitempty"`
	Routers         map[string]RouterSpec     `json:"routers,omitempty" yaml:"routers,omitempty"`
	Services        map[string]ServiceSpec    `json:"services,omitempty" yaml:"services,omitempty"`
	Middlewares     map[string]MiddlewareSpec `json:"middlewares,omitempty" yaml:"middlewares,omitempty"`
}

type RouterSpec struct {
	Name        string   `json:"name" yaml:"name"`
	Rule        string   `json:"rule,omitempty" yaml:"rule,omitempty"`
	EntryPoints []string `json:"entry_points,omitempty" yaml:"entry_points,omitempty"`
	TLS         *TLSSpec `json:"tls,omitempty" yaml:"tls,omitempty"`
	Middlewares []string `json:"middlewares,omitempty" yaml:"middlewares,omitempty"`
	Service     string   `json:"service,omitempty" yaml:"service,omitempty"`
	Priority    *int     `json:"priority,omitempty" yaml:"priority,omitempty"`
}

type TLSSpec struct {
	CertResolver string `json:"cert_resolver,omitempty" yaml:"cert_resolver,omitempty"`
}

type ServiceSpec struct {
	Name           string `json:"name" yaml:"name"`
	Port           int    `json:"port,omitempty" yaml:"port,omitempty"`
	BackendURL     string `json:"backend_url" yaml:"backend_url"`
	BackendSource  string `json:"backend_source,omitempty" yaml:"backend_source,omitempty"`
	PassHostHeader *bool  `json:"pass_host_header,omitempty" yaml:"pass_host_header,omitempty"`
	Sticky         *bool  `json:"sticky,omitempty" yaml:"sticky,omitempty"`
}

type MiddlewareSpec struct {
	Name                string          `json:"name" yaml:"name"`
	RedirectScheme      *RedirectScheme `json:"redirect_scheme,omitempty" yaml:"redirect_scheme,omitempty"`
	Headers             *HeadersSpec    `json:"headers,omitempty" yaml:"headers,omitempty"`
	BasicAuthUsers      []string        `json:"basic_auth_users,omitempty" yaml:"basic_auth_users,omitempty"`
	StripPrefixPrefixes []string        `json:"strip_prefix_prefixes,omitempty" yaml:"strip_prefix_prefixes,omitempty"`
}

type RedirectScheme struct {
	Scheme    string `json:"scheme" yaml:"scheme"`
	Permanent *bool  `json:"permanent,omitempty" yaml:"permanent,omitempty"`
}

type HeadersSpec struct {
	CustomRequestHeaders         map[string]string `json:"custom_request_headers,omitempty" yaml:"custom_request_headers,omitempty"`
	CustomResponseHeaders        map[string]string `json:"custom_response_headers,omitempty" yaml:"custom_response_headers,omitempty"`
	SSLRedirect                  *bool             `json:"ssl_redirect,omitempty" yaml:"ssl_redirect,omitempty"`
	STSSeconds                   *int              `json:"sts_seconds,omitempty" yaml:"sts_seconds,omitempty"`
	STSIncludeSubdomains         *bool             `json:"sts_include_subdomains,omitempty" yaml:"sts_include_subdomains,omitempty"`
	STSPreload                   *bool             `json:"sts_preload,omitempty" yaml:"sts_preload,omitempty"`
	ForceSTSHeader               *bool             `json:"force_sts_header,omitempty" yaml:"force_sts_header,omitempty"`
	BrowserXSSFilter             *bool             `json:"browser_xss_filter,omitempty" yaml:"browser_xss_filter,omitempty"`
	ContentTypeNosniff           *bool             `json:"content_type_nosniff,omitempty" yaml:"content_type_nosniff,omitempty"`
	FrameDeny                    *bool             `json:"frame_deny,omitempty" yaml:"frame_deny,omitempty"`
	AccessControlAllowOriginList []string          `json:"access_control_allow_origin_list,omitempty" yaml:"access_control_allow_origin_list,omitempty"`
	AccessControlAllowMethods    []string          `json:"access_control_allow_methods,omitempty" yaml:"access_control_allow_methods,omitempty"`
	AccessControlAllowHeaders    []string          `json:"access_control_allow_headers,omitempty" yaml:"access_control_allow_headers,omitempty"`
	AccessControlExposeHeaders   []string          `json:"access_control_expose_headers,omitempty" yaml:"access_control_expose_headers,omitempty"`
	AccessControlMaxAge          string            `json:"access_control_max_age,omitempty" yaml:"access_control_max_age,omitempty"`
	AddVaryHeader                *bool             `json:"add_vary_header,omitempty" yaml:"add_vary_header,omitempty"`
}

type ValidationIssue struct {
	WorkerID    string `json:"worker_id,omitempty" yaml:"worker_id,omitempty"`
	ContainerID string `json:"container_id" yaml:"container_id"`
	Container   string `json:"container,omitempty" yaml:"container,omitempty"`
	Scope       string `json:"scope" yaml:"scope"`
	Field       string `json:"field" yaml:"field"`
	Message     string `json:"message" yaml:"message"`
}
