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
	UseTLS           bool
}

type ReceiverConfig struct {
	ListenAddr       string
	Token            string
	StateDir         string
	RenderDir        string
	RequestWindow    time.Duration
	MaxBodyBytes     int64
	StateTTL         time.Duration
	HTTPReadTimeout  time.Duration
	HTTPWriteTimeout time.Duration
}

type TLSConfig struct {
	CertFile string
	KeyFile  string
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
		RenderDir:        envOr("RECEIVER_RENDER_DIR", "./render"),
		RequestWindow:    envDuration("RECEIVER_REQUEST_WINDOW", 5*time.Minute),
		MaxBodyBytes:     envInt64("RECEIVER_MAX_BODY_BYTES", 1<<20),
		StateTTL:         envDuration("RECEIVER_STATE_TTL", 15*time.Minute),
		HTTPReadTimeout:  envDuration("RECEIVER_HTTP_READ_TIMEOUT", 5*time.Second),
		HTTPWriteTimeout: envDuration("RECEIVER_HTTP_WRITE_TIMEOUT", 15*time.Second),
	}
	tls := TLSConfig{
		CertFile: os.Getenv("RECEIVER_TLS_CERT_FILE"),
		KeyFile:  os.Getenv("RECEIVER_TLS_KEY_FILE"),
	}
	fs.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "listen address")
	fs.StringVar(&cfg.Token, "token", cfg.Token, "shared bearer token")
	fs.StringVar(&cfg.StateDir, "state-dir", cfg.StateDir, "durable state directory")
	fs.StringVar(&cfg.RenderDir, "render-dir", cfg.RenderDir, "traefik config output directory")
	fs.DurationVar(&cfg.RequestWindow, "request-window", cfg.RequestWindow, "allowed request timestamp skew")
	fs.Int64Var(&cfg.MaxBodyBytes, "max-body-bytes", cfg.MaxBodyBytes, "maximum request body size")
	fs.DurationVar(&cfg.StateTTL, "state-ttl", cfg.StateTTL, "age after which worker state expires")
	fs.DurationVar(&cfg.HTTPReadTimeout, "http-read-timeout", cfg.HTTPReadTimeout, "http read timeout")
	fs.DurationVar(&cfg.HTTPWriteTimeout, "http-write-timeout", cfg.HTTPWriteTimeout, "http write timeout")
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
