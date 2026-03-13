package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
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
		MySQLPort: cfg.GreptimeDBMySQLPort,
		Logger:    logger,
	})
	if err != nil {
		logger.Error("failed to start greptimedb", "err", err)
		os.Exit(1)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = gdb.Stop(stopCtx)
	}()

	// Step 3: ensure pricing table exists and seed model pricing.
	if err := greptimedb.SeedPricing(cfg.GreptimeDBHTTPPort, logger); err != nil {
		logger.Warn("seed pricing warning", "err", err)
	}

	// Step 4: start HTTP server (dashboard + API proxy).
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

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)

		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = gdb.Stop(stopCtx)
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
