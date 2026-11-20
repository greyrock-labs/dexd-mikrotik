package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/doonga/dexd-mikrotik/internal/config"
	"github.com/doonga/dexd-mikrotik/internal/controller"
	appmetrics "github.com/doonga/dexd-mikrotik/internal/metrics"
	"github.com/doonga/dexd-mikrotik/internal/provider/mikrotik"
	"github.com/doonga/dexd-mikrotik/internal/source"
)

// Build-time metadata populated by -ldflags "-X main.Version=… -X main.Gitsha=…".
var (
	Version = "dev"
	Gitsha  = "dev"
)

// dockerAdapter wraps a *source.DockerSource so it satisfies controller.Source,
// translating Docker SDK events into the controller's domain Event type.
type dockerAdapter struct {
	*source.DockerSource
}

func (a dockerAdapter) Events(ctx context.Context) (<-chan controller.Event, <-chan error) {
	raw, errCh := a.DockerSource.Events(ctx)
	out := make(chan controller.Event)
	go func() {
		defer close(out)
		for m := range raw {
			ev := controller.Event{Action: string(m.Action), Name: m.Actor.Attributes["name"]}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, errCh
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(1)
	}

	setupLogging(cfg)

	slog.Info("dexd-mikrotik starting",
		"version", Version,
		"gitsha", Gitsha,
		"owner_id", cfg.OwnerID,
		"txt_prefix", cfg.TxtPrefix,
		"mikrotik_baseurl", cfg.MikrotikBaseURL,
		"mikrotik_username", cfg.MikrotikUsername,
		"mikrotik_skip_tls_verify", cfg.MikrotikSkipTLSVerify,
		"default_target", cfg.DefaultTarget,
		"default_ttl", cfg.DefaultTTL.String(),
		"policy", cfg.Policy,
		"docker_host", cfg.DockerHost,
		"reconcile_interval", cfg.ReconcileInterval,
		"metrics_addr", cfg.MetricsAddr,
		"dry_run", cfg.DryRun,
	)
	if cfg.CleartextBaseURL() {
		slog.Warn("MIKROTIK_BASEURL uses http://; RouterOS credentials are sent in cleartext, use https:// (www-ssl)")
	}
	appmetrics.SetBuildInfo(Version, Gitsha, cfg.Policy)

	dockerSrc, err := source.NewDockerSource(cfg.DockerHost, cfg.DefaultTarget, cfg.OwnerID)
	if err != nil {
		slog.Error("failed to create docker client", "err", err)
		os.Exit(1)
	}
	defer dockerSrc.Close()

	rosClient := mikrotik.NewClient(mikrotik.Options{
		BaseURL:       cfg.MikrotikBaseURL,
		Username:      cfg.MikrotikUsername,
		Password:      cfg.MikrotikPassword,
		SkipTLSVerify: cfg.MikrotikSkipTLSVerify,
		DryRun:        cfg.DryRun,
		DefaultTTL:    int(cfg.DefaultTTL),
	})

	probeCtx, probeCancel := context.WithTimeout(context.Background(), 15*time.Second)
	res, err := rosClient.SystemResource(probeCtx)
	probeCancel()
	if err != nil {
		slog.Error("cannot reach RouterOS", "url", cfg.MikrotikBaseURL, "err", err)
		os.Exit(1)
	}
	slog.Info("connected to RouterOS", "routeros_version", res.Version, "board", res.BoardName)

	ctrl := controller.New(dockerAdapter{dockerSrc}, rosClient, cfg.OwnerID, cfg.TxtPrefix, cfg.Policy, cfg.ReconcileInterval)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	metricsServer := startMetricsServer(cfg.MetricsAddr)

	ctrl.Run(ctx)

	if metricsServer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := metricsServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("metrics server shutdown failed", "err", err)
		}
	}
	slog.Info("shutdown complete")
}

func setupLogging(cfg *config.Config) {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	var handler slog.Handler
	if cfg.LogFormat == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(handler))
}

func startMetricsServer(addr string) *http.Server {
	if addr == "" {
		slog.Info("metrics server disabled")
		return nil
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", appmetrics.Handler())
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		slog.Info("metrics server listening", "addr", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("metrics server failed", "err", err)
		}
	}()
	return server
}
