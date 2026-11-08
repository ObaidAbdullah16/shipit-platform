// Command api serves the ShipIt link shortener.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ObaidAbdullah16/shipit-platform/services/api/internal/config"
	"github.com/ObaidAbdullah16/shipit-platform/services/api/internal/httpapi"
	"github.com/ObaidAbdullah16/shipit-platform/services/api/internal/redisx"
	"github.com/ObaidAbdullah16/shipit-platform/services/api/internal/store"
)

// version is set at build time with -ldflags "-X main.version=<git sha>".
var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})).With("service", "shipit-api", "version", version)
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.DB.Close()

	// The database may still be starting (docker compose / first deploy): retry for ~60s.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for {
		if err = st.Migrate(ctx); err == nil {
			break
		}
		log.Warn("waiting for database", "err", err)
		select {
		case <-ctx.Done():
			return errors.New("database not reachable after 60s: " + err.Error())
		case <-time.After(3 * time.Second):
		}
	}

	cache := redisx.New(cfg.RedisAddr)
	defer cache.Close()

	srv := httpapi.New(st, cache, log, version, cfg.PublicBaseURL, cfg.CreatePerMinute, cfg.ChaosFailHealth)
	httpSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", httpSrv.Addr, "chaos_fail_health", cfg.ChaosFailHealth)
		errCh <- httpSrv.ListenAndServe()
	}()

	// ECS sends SIGTERM before stopping a task: finish in-flight requests first.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, os.Interrupt)
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case sig := <-stop:
		log.Info("shutting down", "signal", sig.String())
		sctx, scancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer scancel()
		return httpSrv.Shutdown(sctx)
	}
	return nil
}
