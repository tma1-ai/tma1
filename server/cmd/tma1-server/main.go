package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tma1-ai/tma1/server/internal/config"
	"github.com/tma1-ai/tma1/server/internal/greptimedb"
	"github.com/tma1-ai/tma1/server/internal/handler"
	"github.com/tma1-ai/tma1/server/internal/hooks"
	"github.com/tma1-ai/tma1/server/internal/install"
	"github.com/tma1-ai/tma1/server/internal/mcp"
	"github.com/tma1-ai/tma1/server/internal/perception"
	"github.com/tma1-ai/tma1/server/internal/sensor/build"
	"github.com/tma1-ai/tma1/server/internal/transcript"
)

// Version is set at build time via -ldflags "-X main.Version=<tag>".
var Version = "dev"

func main() {
	// Subcommand routing. The default (no args) runs the full HTTP server.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "mcp-serve":
			// MCP stdio server. Stdout is reserved for JSON-RPC; logs → stderr.
			if err := runMCPServe(); err != nil {
				fmt.Fprintf(os.Stderr, "mcp-serve: %v\n", err)
				os.Exit(1)
			}
			return
		case "install":
			if err := runInstall(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "install: %v\n", err)
				os.Exit(1)
			}
			return
		case "build":
			exitCode, err := runBuild(os.Args[2:])
			if err != nil {
				fmt.Fprintf(os.Stderr, "build: %v\n", err)
				os.Exit(1)
			}
			os.Exit(exitCode)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	// Apply persisted settings (env vars take priority).
	settings := config.LoadSettings(cfg.DataDir)
	config.ApplySettings(cfg, settings)

	var logLevel slog.LevelVar
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		logLevel.Set(slog.LevelDebug)
	case "warn":
		logLevel.Set(slog.LevelWarn)
	case "error":
		logLevel.Set(slog.LevelError)
	default:
		logLevel.Set(slog.LevelInfo)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: &logLevel,
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

	// Step 3.1: create session tables (hooks + transcript — no dependency on trace data).
	if err := greptimedb.InitSessionTables(cfg.GreptimeDBHTTPPort, logger); err != nil {
		logger.Warn("session table creation failed", "err", err)
	}

	// Step 3.2: create build sensor table (tma1_build_events).
	if err := greptimedb.InitBuildTable(cfg.GreptimeDBHTTPPort, logger); err != nil {
		logger.Warn("build_events table creation failed", "err", err)
	}

	// Step 3.3: create git/file sensor table (tma1_external_changes).
	if err := greptimedb.InitExternalChangesTable(cfg.GreptimeDBHTTPPort, logger); err != nil {
		logger.Warn("external_changes table creation failed", "err", err)
	}

	// Step 3.4: create project sensor table (tma1_project_state).
	if err := greptimedb.InitProjectStateTable(cfg.GreptimeDBHTTPPort, logger); err != nil {
		logger.Warn("project_state table creation failed", "err", err)
	}

	// Step 3.4.1: create anomaly emit log (drives 1.7 validation gates).
	if err := greptimedb.InitAnomalyEmitsTable(cfg.GreptimeDBHTTPPort, logger); err != nil {
		logger.Warn("anomaly_emits table creation failed", "err", err)
	}

	// Step 3.5: check for tma1-server upgrade.
	// No version file (old == "") covers both fresh install and old-version upgrade;
	// we cannot distinguish the two, so we always attempt truncate+reseed.
	// On fresh install the table doesn't exist yet — TruncatePricing returns a
	// benign "table not found" error, which we ignore (SeedPricing creates it).
	versionFile := filepath.Join(cfg.DataDir, ".tma1-version")
	upgraded := false
	var upgradeErr error
	if Version != "dev" {
		old := readVersionFile(versionFile)
		if old != Version {
			upgraded = true
			if old != "" {
				logger.Info("tma1 upgrade detected", "from", old, "to", Version)
			}
			if err := onUpgrade(cfg.GreptimeDBHTTPPort, logger); err != nil {
				if greptimedb.IsTableNotFound(err) {
					logger.Info("pricing table does not exist yet (fresh install), skipping truncate")
				} else {
					upgradeErr = err
					logger.Warn("truncate pricing on upgrade failed, will retry next start", "err", err)
				}
			}
		}
	}

	// Step 4: ensure pricing table exists and seed model pricing.
	seedErr := greptimedb.SeedPricing(cfg.GreptimeDBHTTPPort, logger)
	if seedErr != nil {
		logger.Warn("seed pricing warning", "err", seedErr)
	}

	// Step 4.5: post-upgrade — write version file + best-effort cost flow recreate.
	// Version file gates on truncate+seed only; cost flow creation is best-effort
	// because it requires opentelemetry_traces which may not exist yet.
	// initFlowsWithRetry (Step 5) handles deferred cost flow creation.
	if upgraded && upgradeErr == nil && seedErr == nil {
		if err := greptimedb.InitCostFlow(cfg.GreptimeDBHTTPPort, logger); err != nil {
			logger.Warn("cost flow creation deferred to background retry", "err", err)
		}
		if err := os.WriteFile(versionFile, []byte(Version), 0o644); err != nil {
			logger.Warn("failed to write version file", "err", err)
		}
	}

	// Step 5: initialize flow aggregations (background retry).
	// Flows depend on opentelemetry_traces which is auto-created when the
	// first trace arrives. Sink table DDL always succeeds (IF NOT EXISTS),
	// but CREATE FLOW fails until the source table exists. We retry
	// periodically so flows are created once trace data arrives.
	flowCtx, flowCancel := context.WithCancel(context.Background())
	defer flowCancel()
	go initFlowsWithRetry(flowCtx, cfg.GreptimeDBHTTPPort, logger)

	// Step 6: install hook script + create transcript watcher.
	portNum := 14318
	if p, err := parsePort(cfg.Port); err == nil {
		portNum = p
	}
	hookPath, err := hooks.EnsureHookScript(cfg.DataDir, portNum, logger)
	if err != nil {
		logger.Warn("hook script install failed", "err", err)
	} else {
		logger.Info("hook script ready — configure in ~/.claude/settings.json", "path", hookPath)
	}

	bc := handler.NewHookBroadcaster()
	tw := transcript.NewWatcher(cfg.GreptimeDBHTTPPort, logger, bc.Broadcast)
	defer tw.StopAll()

	// Start Codex session scanner (discovers ~/.codex/sessions/ JSONL files).
	codexCtx, codexCancel := context.WithCancel(context.Background())
	defer codexCancel()
	go tw.StartCodexScanner(codexCtx)

	// Start OpenClaw session scanner (discovers ~/.openclaw/agents/*/sessions/ JSONL files).
	openclawCtx, openclawCancel := context.WithCancel(context.Background())
	defer openclawCancel()
	go tw.StartOpenClawScanner(openclawCtx)

	// Start Copilot CLI session scanner (discovers ~/.copilot/session-state/*/events.jsonl).
	copilotCLICtx, copilotCLICancel := context.WithCancel(context.Background())
	defer copilotCLICancel()
	go tw.StartCopilotCLIScanner(copilotCLICtx)

	// Step 7: start HTTP server (dashboard + API proxy).
	llmCfg := handler.LLMConfig{
		APIKey:   cfg.LLMAPIKey,
		Provider: cfg.LLMProvider,
		Model:    cfg.LLMModel,
	}
	srv := handler.New(cfg.GreptimeDBHTTPPort, cfg.Port, webFileSystem(), logger, tw, bc, llmCfg, handler.ServerConfig{
		DataDir:          cfg.DataDir,
		DataTTL:          cfg.DataTTL,
		QueryConcurrency: cfg.QueryConcurrency,
		LogLevelVar:      &logLevel,
	})
	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()
	srv.StartBackgroundTasks(bgCtx)
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
		bgCancel()
		codexCancel()
		openclawCancel()
		copilotCLICancel()
		tw.StopAll()

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

// runBuild handles `tma1-server build [flags] -- <command> [args...]`.
//
// Captures the subprocess's stdout/stderr, tees them to the user's terminal,
// and writes batched output to tma1_build_events so agents can query build
// status through perception (get_build_status / get_context_bundle).
//
// Flags:
//
//	--watch                 run as a long-running watcher with debounced flushes
//	--debounce 2s           flush interval for --watch (default 2s)
//	--filter-regex PATTERN  only capture lines matching the regex
//	--filter-invert         invert the regex match (capture non-matching lines)
//	--tag NAME              override the short identifier (default: command name)
//	--project DIR           project label override (default: ResolveProjectRoot(cwd) basename)
func runBuild(args []string) (int, error) {
	cfg, err := config.Load()
	if err != nil {
		return 1, fmt.Errorf("load config: %w", err)
	}

	// Parse flags up to `--`.
	var (
		watch         bool
		debounceStr   = "2s"
		filterRegex   string
		filterInvert  bool
		tag           string
		projectOverride string
	)

	i := 0
	for i < len(args) {
		switch args[i] {
		case "--watch":
			watch = true
			i++
		case "--debounce":
			if i+1 >= len(args) {
				return 1, fmt.Errorf("--debounce requires a value")
			}
			debounceStr = args[i+1]
			i += 2
		case "--filter-regex":
			if i+1 >= len(args) {
				return 1, fmt.Errorf("--filter-regex requires a value")
			}
			filterRegex = args[i+1]
			i += 2
		case "--filter-invert":
			filterInvert = true
			i++
		case "--tag":
			if i+1 >= len(args) {
				return 1, fmt.Errorf("--tag requires a value")
			}
			tag = args[i+1]
			i += 2
		case "--project":
			if i+1 >= len(args) {
				return 1, fmt.Errorf("--project requires a value")
			}
			projectOverride = args[i+1]
			i += 2
		case "--":
			i++
			goto runCmd
		case "-h", "--help":
			fmt.Println("usage: tma1-server build [flags] -- <command> [args...]")
			fmt.Println("flags: --watch --debounce 2s --filter-regex PAT --filter-invert --tag NAME --project DIR")
			return 0, nil
		default:
			// Treat the first non-flag positional as the start of the command.
			goto runCmd
		}
	}

runCmd:
	cmdArgs := args[i:]
	if len(cmdArgs) == 0 {
		return 1, fmt.Errorf("no command provided after --")
	}

	debounce, err := time.ParseDuration(debounceStr)
	if err != nil {
		return 1, fmt.Errorf("--debounce: %w", err)
	}

	filter, err := build.RegexFilter(filterRegex, filterInvert)
	if err != nil {
		return 1, err
	}

	project := projectOverride
	if project == "" {
		cwd, _ := os.Getwd()
		root := perception.ResolveProjectRoot(cwd)
		project = filepath.Base(root)
	}

	// Ensure tma1_build_events exists. Idempotent; cheap; lets a fresh user
	// run `tma1-server build` standalone without waiting for the long-running
	// tma1-server to have created it first.
	if err := greptimedb.InitBuildTable(cfg.GreptimeDBHTTPPort,
		slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		fmt.Fprintf(os.Stderr, "tma1 build: warning: could not ensure table: %v\n", err)
	}

	store := build.NewGreptimeStore(cfg.GreptimeDBHTTPPort)
	bcfg := build.Config{
		Project: project,
		Command: strings.Join(cmdArgs, " "),
		Tag:     tag,
		Filter:  filter,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if watch {
		result, err := build.NewLongRunner(store, bcfg, debounce).Run(ctx, cmdArgs)
		if err != nil {
			return 1, err
		}
		return result.ExitCode, nil
	}
	result, err := build.NewRunner(store, bcfg).Run(ctx, cmdArgs)
	if err != nil {
		return 1, err
	}
	return result.ExitCode, nil
}

// runInstall handles `tma1-server install [--adapter claude-code] [--project DIR] [--dry-run]`.
// Prints a human-readable report to stdout.
//
// --dry-run shows what would change without touching disk; intended for
// users wary of an installer that writes to ~/.claude.json (OAuth tokens
// live there). Every file write inside the installer routes through a
// single sink that respects this flag.
func runInstall(args []string) error {
	adapter := "claude-code"
	project, _ := os.Getwd()
	dryRun := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--adapter":
			if i+1 >= len(args) {
				return fmt.Errorf("--adapter needs a value")
			}
			adapter = args[i+1]
			i++
		case "--project":
			if i+1 >= len(args) {
				return fmt.Errorf("--project needs a value")
			}
			project = args[i+1]
			i++
		case "--dry-run", "-n":
			dryRun = true
		case "-h", "--help":
			fmt.Println("usage: tma1-server install [--adapter claude-code] [--project DIR] [--dry-run]")
			return nil
		default:
			return fmt.Errorf("unknown flag %q", args[i])
		}
	}

	if adapter != "claude-code" {
		return fmt.Errorf("adapter %q not supported yet (only claude-code)", adapter)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	port := 14318
	if p, err := strconv.Atoi(cfg.Port); err == nil {
		port = p
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	inst := &hooks.ClaudeCodeInstaller{
		DataDir:            cfg.DataDir,
		Port:               port,
		GreptimeDBHTTPPort: cfg.GreptimeDBHTTPPort,
		ProjectDir:         project,
		Logger:             logger,
		DryRun:             dryRun,
	}
	rep, installErr := inst.Install()

	if dryRun {
		fmt.Printf("TMA1 install report (adapter=%s, DRY-RUN — no files touched)\n", adapter)
	} else {
		fmt.Printf("TMA1 install report (adapter=%s)\n", adapter)
	}
	fmt.Printf("  Hook script:   %s\n", rep.HookScript)
	fmt.Printf("  Settings:      %s\n", rep.SettingsPath)
	if rep.InstructionsPath != "" {
		fmt.Printf("  Instructions:  %s\n", rep.InstructionsPath)
	}
	if rep.GitignorePath != "" {
		fmt.Printf("  .gitignore:    %s\n", rep.GitignorePath)
	}
	if rep.MCPConfigPath != "" {
		fmt.Printf("  MCP config:    %s\n", rep.MCPConfigPath)
	}
	if len(rep.SkillPaths) > 0 {
		fmt.Println("  Skills:")
		for _, p := range rep.SkillPaths {
			fmt.Printf("    - %s\n", p)
		}
	}
	if len(rep.CommandPaths) > 0 {
		fmt.Println("  Commands:")
		for _, p := range rep.CommandPaths {
			fmt.Printf("    - %s\n", p)
		}
	}
	if len(rep.Changed) == 0 {
		fmt.Println("  No changes (already installed).")
	} else {
		header := "  Changed:"
		if dryRun {
			header = "  Would change:"
		}
		fmt.Println(header)
		for _, c := range rep.Changed {
			fmt.Printf("    - %s\n", c)
		}
	}
	if installErr != nil {
		return installErr
	}
	return nil
}

// runMCPServe handles the `tma1-server mcp-serve` subcommand. Claude Code
// spawns this once per session to talk MCP over stdio. The function MUST NOT
// write anything to stdout that isn't a JSON-RPC frame.
//
// mcp-serve does NOT start GreptimeDB — it connects to the running parent
// tma1-server's GreptimeDB on cfg.GreptimeDBHTTPPort (default 14000).
func runMCPServe() error {
	mcp.ServerVersion = Version

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Logger writes to stderr — stdout is owned by the JSON-RPC stream.
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	bundler := perception.NewBundler(cfg.GreptimeDBHTTPPort, logger)

	srv := mcp.NewServer(logger,
		mcp.ContextBundleTool{Bundler: bundler},
		mcp.SessionStateTool{Bundler: bundler},
		mcp.AnomaliesTool{Bundler: bundler},
		mcp.BuildStatusTool{Bundler: bundler},
		mcp.ExternalChangesTool{Bundler: bundler},
		mcp.ProjectStateTool{Bundler: bundler},
		mcp.PeerSessionsTool{Bundler: bundler},
	)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return srv.Run(ctx)
}

func readVersionFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func parsePort(s string) (int, error) {
	return strconv.Atoi(s)
}

func onUpgrade(httpPort int, logger *slog.Logger) error {
	// Clear stale pricing so SeedPricing re-inserts with latest data.
	return greptimedb.TruncatePricing(httpPort)
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
			if err := greptimedb.InitFlows(httpPort, logger); err != nil {
				logger.Warn("flow creation failed, will retry", "err", err)
			}
			if err := greptimedb.InitCostFlow(httpPort, logger); err != nil {
				logger.Warn("cost flow creation failed, will retry", "err", err)
			}
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
