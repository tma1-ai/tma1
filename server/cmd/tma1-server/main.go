package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tma1-ai/tma1/server/internal/config"
	"github.com/tma1-ai/tma1/server/internal/greptimedb"
	"github.com/tma1-ai/tma1/server/internal/handler"
	"github.com/tma1-ai/tma1/server/internal/install"
)

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(cfg.LogLevel),
	}))

	// Step 1: ensure GreptimeDB binary is present.
	binPath, err := install.EnsureGreptimeDB(cfg.DataDir, cfg.GreptimeDBVersion, logger)
	if err != nil {
		logger.Error("failed to install greptimedb", "err", err)
		os.Exit(1)
	}

	// Step 2: start GreptimeDB child process.
	gdb, err := greptimedb.Start(greptimedb.Config{
		BinPath:   binPath,
		DataDir:   cfg.DataDir,
		HTTPPort:  cfg.GreptimeDBHTTPPort,
		GRPCPort:  cfg.GreptimeDBGRPCPort,
		MySQLPort: cfg.GreptimeDBMySQLPort,
		Logger:    logger,
	})
	if err != nil {
		logger.Error("failed to start greptimedb", "err", err)
		os.Exit(1)
	}
	var stopOnce sync.Once
	stopGDB := func() {
		stopOnce.Do(func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = gdb.Stop(stopCtx)
		})
	}
	defer stopGDB()

	// Step 3: set database default TTL (before pricing/flows so new tables inherit it).
	if err := greptimedb.SetDatabaseTTL(cfg.GreptimeDBHTTPPort, cfg.DataTTL, logger); err != nil {
		logger.Warn("set database TTL warning", "err", err)
	}

	// Step 4: ensure pricing table exists and seed model pricing.
	if err := greptimedb.SeedPricing(cfg.GreptimeDBHTTPPort, logger); err != nil {
		logger.Warn("seed pricing warning", "err", err)
	}

	// Step 5: initialize flow aggregations (background retry).
	// Flows depend on opentelemetry_traces which is auto-created when the
	// first trace arrives. Sink table DDL always succeeds (IF NOT EXISTS),
	// but CREATE FLOW fails until the source table exists. We retry
	// periodically so flows are created once trace data arrives.
	flowCtx, flowCancel := context.WithCancel(context.Background())
	defer flowCancel()
	go initFlowsWithRetry(flowCtx, cfg.GreptimeDBHTTPPort, logger)

	// Step 6: start HTTP server (dashboard + API proxy).
	srv := handler.New(cfg.GreptimeDBHTTPPort, cfg.Port, webFileSystem(), logger)
	httpSrv := &http.Server{
		Addr:         cfg.Host + ":" + cfg.Port,
		Handler:      srv.Router(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		logger.Info("received signal, shutting down", "signal", sig)

		flowCancel()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)

		stopGDB()
	}()

	logger.Info("tma1 dashboard ready",
		"url", "http://localhost:"+cfg.Port,
		"otlp_endpoint", "http://localhost:"+cfg.Port+"/v1/otlp",
	)

	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server error", "err", err)
		os.Exit(1)
	}
	logger.Info("tma1-server stopped")
}

// initFlowsWithRetry attempts to create flow aggregations up to 10 times
// (~5 minutes). Skips if all flows already exist. Only attempts creation
// when GenAI trace data is present (flows depend on gen_ai.* columns).
func initFlowsWithRetry(ctx context.Context, httpPort int, logger *slog.Logger) {
	for i := 0; i < 10; i++ {
		// Re-attempt pricing seed in case it failed at startup.
		if err := greptimedb.SeedPricing(httpPort, logger); err != nil {
			logger.Warn("seed pricing retry warning", "err", err)
		}
		if greptimedb.FlowsReady(httpPort) {
			logger.Info("all flows already exist, skipping init")
			return
		}
		if greptimedb.HasGenAITraces(httpPort) {
			logger.Info("GenAI trace data detected, creating flows")
			greptimedb.InitFlows(httpPort, logger)
			greptimedb.InitCostFlow(httpPort, logger)
		}
		if i < 9 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
	}
}
