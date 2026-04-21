package runtimeinfo

import (
	"os"
	"runtime"
	"time"
)

type Info struct {
	Component string    `json:"component"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
	PID       int       `json:"pid"`
	GoVersion string    `json:"go_version"`
}

var startedAt = time.Now().UTC()

func Current(component string) Info {
	version := os.Getenv("TRAEFIK_CONNECT_VERSION")
	if version == "" {
		version = "dev"
	}
	return Info{
		Component: component,
		Version:   version,
		StartedAt: startedAt,
		PID:       os.Getpid(),
		GoVersion: runtime.Version(),
	}
}
