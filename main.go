package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/tobydoescode/tailscale-funnel-manager/internal/api"
	"github.com/tobydoescode/tailscale-funnel-manager/internal/auth"
	"github.com/tobydoescode/tailscale-funnel-manager/internal/kube"
	"github.com/tobydoescode/tailscale-funnel-manager/internal/manager"
	"github.com/tobydoescode/tailscale-funnel-manager/internal/metrics"
	"github.com/tobydoescode/tailscale-funnel-manager/internal/web"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	token := os.Getenv("FUNNEL_MANAGER_TOKEN")
	if token == "" {
		slog.Error("FUNNEL_MANAGER_TOKEN env var is required")
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
	kubeconfig := kubeconfigPath()

	reconcileInterval := 60 * time.Second
	if v := os.Getenv("FUNNEL_MANAGER_RECONCILE_INTERVAL"); v != "" {
		parsed, err := time.ParseDuration(v)
		if err != nil {
			slog.Error("invalid FUNNEL_MANAGER_RECONCILE_INTERVAL", "value", v, "err", err)
			os.Exit(1)
		}
		reconcileInterval = parsed
	}

	client, err := kube.NewClient(kubeconfig)
	if err != nil {
		slog.Error("failed to build kubernetes client", "err", err)
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
			// Log the detail; the response body must not leak cluster
			// internals to unauthenticated callers.
			slog.Error("readiness check failed", "err", err)
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	mux.Handle("GET /metrics", metrics.Handler())

	authed := auth.Bearer(token)
	mux.Handle("GET /api/services", authed(http.HandlerFunc(handler.ListServices)))
	mux.Handle("POST /api/services/{namespace}/{name}/funnel", authed(http.HandlerFunc(handler.SetFunnel)))

	srv := &http.Server{
		Addr:              addr,
		Handler:           securityHeaders(observe(mux)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	reconciler := &manager.Reconciler{Client: client, DefaultTags: defaultTags, Interval: reconcileInterval}
	reconcilerDone := make(chan struct{})
	go func() {
		reconciler.Run(ctx)
		close(reconcilerDone)
	}()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		slog.Error("server error", "err", err)
		os.Exit(1)
	case <-ctx.Done():
		slog.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "err", err)
		os.Exit(1)
	}
	<-reconcilerDone
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// observe counts every request in metrics and access-logs everything except
// probe and scrape endpoints, which would drown the log.
func observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		metrics.HTTPRequests.Inc(route, strconv.Itoa(sw.status))
		switch r.URL.Path {
		case "/healthz", "/readyz", "/metrics":
			return
		}
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

func kubeconfigPath() string {
	if v := os.Getenv("FUNNEL_MANAGER_KUBECONFIG"); v != "" {
		return v
	}
	return os.Getenv("KUBECONFIG")
}
