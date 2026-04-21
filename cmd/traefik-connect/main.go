package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"traefik-connect/internal/config"
	"traefik-connect/internal/receiver"
	"traefik-connect/internal/worker"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "agent":
		var cfg config.AgentConfig
		cfg, err = config.LoadAgent(os.Args[2:])
		if err == nil {
			var a *worker.Agent
			a, err = worker.NewAgent(cfg, logger)
			if err == nil {
				err = a.Run(ctx)
			}
		}
	case "receiver":
		var cfg config.ReceiverConfig
		var tlsCfg config.TLSConfig
		cfg, tlsCfg, err = config.LoadReceiver(os.Args[2:])
		if err == nil {
			var app *receiver.App
			app, err = receiver.NewApp(cfg, cfg.Token, logger)
			if err == nil {
				err = app.Run(ctx, tlsCfg)
			}
		}
	default:
		usage()
		os.Exit(2)
	}

	if err != nil && err != context.Canceled {
		logger.Error("process failed", "error", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: traefik-connect [agent|receiver] [flags]")
}
