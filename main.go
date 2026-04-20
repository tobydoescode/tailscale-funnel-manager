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

	"github.com/tobydoescode/tailscale-funnel-manager/internal/api"
	"github.com/tobydoescode/tailscale-funnel-manager/internal/auth"
	"github.com/tobydoescode/tailscale-funnel-manager/internal/kube"
	"github.com/tobydoescode/tailscale-funnel-manager/internal/manager"
	"github.com/tobydoescode/tailscale-funnel-manager/internal/web"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	token := os.Getenv("FUNNEL_MANAGER_TOKEN")
	if token == "" {
		logger.Error("FUNNEL_MANAGER_TOKEN env var is required")
		os.Exit(1)
	}

	addr := os.Getenv("FUNNEL_MANAGER_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	defaultTags := os.Getenv("FUNNEL_MANAGER_DEFAULT_TAGS")
	if defaultTags == "" {
		defaultTags = "tag:live-k3s-funnel"
	}

	tailnet := os.Getenv("FUNNEL_MANAGER_TAILNET")

	reconcileInterval := 60 * time.Second
	if v := os.Getenv("FUNNEL_MANAGER_RECONCILE_INTERVAL"); v != "" {
		parsed, err := time.ParseDuration(v)
		if err != nil {
			logger.Error("invalid FUNNEL_MANAGER_RECONCILE_INTERVAL", "value", v, "err", err)
			os.Exit(1)
		}
		reconcileInterval = parsed
	}

	client, err := kube.NewInClusterClient()
	if err != nil {
		logger.Error("failed to build kubernetes client", "err", err)
		os.Exit(1)
	}

	handler := api.NewHandler(api.Config{
		Kube:        client,
		DefaultTags: defaultTags,
		Tailnet:     tailnet,
	})

	mux := http.NewServeMux()
	mux.Handle("GET /", web.Assets())
	mux.Handle("GET /healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	mux.Handle("GET /readyz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := client.Ready(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	authed := auth.Bearer(token)
	mux.Handle("GET /api/services", authed(http.HandlerFunc(handler.ListServices)))
	mux.Handle("POST /api/services/{namespace}/{name}/funnel", authed(http.HandlerFunc(handler.SetFunnel)))

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	reconciler := &manager.Reconciler{Client: client, Interval: reconcileInterval}
	go reconciler.Run(ctx)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		logger.Error("server error", "err", err)
		os.Exit(1)
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "err", err)
		os.Exit(1)
	}
}
