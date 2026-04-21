package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"
)

type AgentConfig struct {
	WorkerID         string
	AdvertiseAddr    string
	DockerSocket     string
	MasterURL        string
	Token            string
	ResyncInterval   time.Duration
	RequestTimeout   time.Duration
	UserAgent        string
	StatusListenAddr string
	ProxyListenAddr  string
	UseTLS           bool
}

type ReceiverConfig struct {
	ListenAddr       string
	Token            string
	StateDir         string
	RequestWindow    time.Duration
	MaxBodyBytes     int64
	StateTTL         time.Duration
	HTTPReadTimeout  time.Duration
	HTTPWriteTimeout time.Duration
	DockerSocket     string
	DockerNetwork    string
	StubImage        string
}

type TLSConfig struct {
	CertFile string
	KeyFile  string
}

type StubConfig struct {
	ListenAddr  string
	TargetURL   string
	Token       string
	ContainerID string
	ServiceName string
}

func LoadAgent(args []string) (AgentConfig, error) {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	cfg := AgentConfig{
		WorkerID:         envOr("AGENT_WORKER_ID", hostnameFallback("worker")),
		AdvertiseAddr:    os.Getenv("AGENT_ADVERTISE_ADDR"),
		DockerSocket:     envOr("AGENT_DOCKER_SOCKET", "/var/run/docker.sock"),
		MasterURL:        os.Getenv("AGENT_MASTER_URL"),
		Token:            os.Getenv("AGENT_TOKEN"),
		ResyncInterval:   envDuration("AGENT_RESYNC_INTERVAL", 30*time.Second),
		RequestTimeout:   envDuration("AGENT_REQUEST_TIMEOUT", 10*time.Second),
		UserAgent:        envOr("AGENT_USER_AGENT", "traefik-connect-agent/1.0"),
		StatusListenAddr: envOr("AGENT_STATUS_LISTEN_ADDR", ":8081"),
		ProxyListenAddr:  envOr("AGENT_PROXY_LISTEN_ADDR", ":8090"),
	}
	fs.StringVar(&cfg.WorkerID, "worker-id", cfg.WorkerID, "worker identifier")
	fs.StringVar(&cfg.AdvertiseAddr, "advertise-addr", cfg.AdvertiseAddr, "worker LAN address")
	fs.StringVar(&cfg.DockerSocket, "docker-socket", cfg.DockerSocket, "path to docker socket")
	fs.StringVar(&cfg.MasterURL, "master-url", cfg.MasterURL, "receiver base URL")
	fs.StringVar(&cfg.Token, "token", cfg.Token, "shared bearer token")
	fs.DurationVar(&cfg.ResyncInterval, "resync-interval", cfg.ResyncInterval, "full resync interval")
	fs.DurationVar(&cfg.RequestTimeout, "request-timeout", cfg.RequestTimeout, "sync request timeout")
	fs.StringVar(&cfg.UserAgent, "user-agent", cfg.UserAgent, "HTTP user agent")
	fs.StringVar(&cfg.StatusListenAddr, "status-listen", cfg.StatusListenAddr, "agent status listen address")
	fs.StringVar(&cfg.ProxyListenAddr, "proxy-listen", cfg.ProxyListenAddr, "agent proxy listen address")
	if err := fs.Parse(args); err != nil {
		return AgentConfig{}, err
	}
	if cfg.WorkerID == "" || cfg.MasterURL == "" || cfg.Token == "" {
		return AgentConfig{}, fmt.Errorf("worker-id, master-url, and token are required")
	}
	return cfg, nil
}

func LoadReceiver(args []string) (ReceiverConfig, TLSConfig, error) {
	fs := flag.NewFlagSet("receiver", flag.ContinueOnError)
	cfg := ReceiverConfig{
		ListenAddr:       envOr("RECEIVER_LISTEN_ADDR", ":8080"),
		Token:            os.Getenv("RECEIVER_TOKEN"),
		StateDir:         envOr("RECEIVER_STATE_DIR", "./state"),
		RequestWindow:    envDuration("RECEIVER_REQUEST_WINDOW", 5*time.Minute),
		MaxBodyBytes:     envInt64("RECEIVER_MAX_BODY_BYTES", 1<<20),
		StateTTL:         envDuration("RECEIVER_STATE_TTL", 15*time.Minute),
		HTTPReadTimeout:  envDuration("RECEIVER_HTTP_READ_TIMEOUT", 5*time.Second),
		HTTPWriteTimeout: envDuration("RECEIVER_HTTP_WRITE_TIMEOUT", 15*time.Second),
		DockerSocket:     envOr("RECEIVER_DOCKER_SOCKET", "/var/run/docker.sock"),
		DockerNetwork:    envOr("RECEIVER_DOCKER_NETWORK", "traefik-connect"),
		StubImage:        envOr("RECEIVER_STUB_IMAGE", "traefik-connect"),
	}
	tls := TLSConfig{
		CertFile: os.Getenv("RECEIVER_TLS_CERT_FILE"),
		KeyFile:  os.Getenv("RECEIVER_TLS_KEY_FILE"),
	}
	fs.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "listen address")
	fs.StringVar(&cfg.Token, "token", cfg.Token, "shared bearer token")
	fs.StringVar(&cfg.StateDir, "state-dir", cfg.StateDir, "durable state directory")
	fs.DurationVar(&cfg.RequestWindow, "request-window", cfg.RequestWindow, "allowed request timestamp skew")
	fs.Int64Var(&cfg.MaxBodyBytes, "max-body-bytes", cfg.MaxBodyBytes, "maximum request body size")
	fs.DurationVar(&cfg.StateTTL, "state-ttl", cfg.StateTTL, "age after which worker state expires")
	fs.DurationVar(&cfg.HTTPReadTimeout, "http-read-timeout", cfg.HTTPReadTimeout, "http read timeout")
	fs.DurationVar(&cfg.HTTPWriteTimeout, "http-write-timeout", cfg.HTTPWriteTimeout, "http write timeout")
	fs.StringVar(&cfg.DockerSocket, "docker-socket", cfg.DockerSocket, "docker socket path")
	fs.StringVar(&cfg.DockerNetwork, "docker-network", cfg.DockerNetwork, "docker network for stub containers")
	fs.StringVar(&cfg.StubImage, "stub-image", cfg.StubImage, "image used for stub containers")
	fs.StringVar(&tls.CertFile, "tls-cert", tls.CertFile, "tls cert file")
	fs.StringVar(&tls.KeyFile, "tls-key", tls.KeyFile, "tls key file")
	if err := fs.Parse(args); err != nil {
		return ReceiverConfig{}, TLSConfig{}, err
	}
	if cfg.Token == "" {
		return ReceiverConfig{}, TLSConfig{}, fmt.Errorf("token is required")
	}
	return cfg, tls, nil
}

func LoadStub(args []string) (StubConfig, error) {
	fs := flag.NewFlagSet("stub", flag.ContinueOnError)
	cfg := StubConfig{
		ListenAddr:  envOr("STUB_LISTEN_ADDR", ":8080"),
		TargetURL:   os.Getenv("STUB_TARGET_URL"),
		Token:       os.Getenv("STUB_TOKEN"),
		ContainerID: os.Getenv("STUB_CONTAINER_ID"),
		ServiceName: os.Getenv("STUB_SERVICE_NAME"),
	}
	fs.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "listen address")
	fs.StringVar(&cfg.TargetURL, "target-url", cfg.TargetURL, "worker proxy target url")
	fs.StringVar(&cfg.Token, "token", cfg.Token, "shared bearer token")
	fs.StringVar(&cfg.ContainerID, "container-id", cfg.ContainerID, "source container id")
	fs.StringVar(&cfg.ServiceName, "service-name", cfg.ServiceName, "source service name")
	if err := fs.Parse(args); err != nil {
		return StubConfig{}, err
	}
	if cfg.TargetURL == "" || cfg.Token == "" || cfg.ContainerID == "" || cfg.ServiceName == "" {
		return StubConfig{}, fmt.Errorf("target-url, token, container-id, and service-name are required")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func envInt64(key string, fallback int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func hostnameFallback(prefix string) string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return prefix
	}
	return h
}
