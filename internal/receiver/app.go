package receiver

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"traefik-connect/internal/config"
)

type App struct {
	cfg    config.ReceiverConfig
	server *Server
	store  *Store
	log    *slog.Logger
}

func NewApp(cfg config.ReceiverConfig, token string, log *slog.Logger) (*App, error) {
	store := NewStore(cfg.StateDir, cfg.RenderDir, cfg.StateTTL, log)
	if err := store.Load(); err != nil {
		return nil, err
	}
	return &App{
		cfg:    cfg,
		server: NewServer(store, token, cfg.RequestWindow, cfg.MaxBodyBytes),
		store:  store,
		log:    log,
	}, nil
}

func (a *App) Run(ctx context.Context, tlsCfg config.TLSConfig) error {
	go a.pruneLoop(ctx)
	srv := &http.Server{
		Addr:              a.cfg.ListenAddr,
		Handler:           a.server.Handler(),
		ReadTimeout:       a.cfg.HTTPReadTimeout,
		WriteTimeout:      a.cfg.HTTPWriteTimeout,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		if tlsCfg.CertFile != "" && tlsCfg.KeyFile != "" {
			errCh <- srv.ListenAndServeTLS(tlsCfg.CertFile, tlsCfg.KeyFile)
			return
		}
		errCh <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		<-errCh
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

func (a *App) pruneLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.store.RemoveExpired(time.Now().UTC()); err != nil {
				a.log.Warn("failed to prune expired workers", "error", err)
			}
		}
	}
}
