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

const rootHelpText = `tma1-server — local-first LLM agent observability

Usage:
  tma1-server [subcommand] [flags]

Without a subcommand, tma1-server runs the long-running HTTP server and
manages a child GreptimeDB process. Dashboard, MCP, and OTLP all live
on localhost.

Subcommands:
  install      Wire a coding agent (claude-code, codex) into TMA1
  uninstall    Reverse install for one adapter
  build        Wrap a build/test command and ship its output to TMA1
  mcp-serve    Run as JSON-RPC MCP stdio server (spawned by agents)
  help [SUB]   Print this help, or details for a specific subcommand
  version      Print the tma1-server version

Flags:
  -h, --help     Show this help
  -v, --version  Show the tma1-server version

Environment:
  TMA1_PORT                  HTTP / OTLP port (default 14318)
  TMA1_DATA_DIR              Data directory (default ~/.tma1)
  TMA1_GREPTIMEDB_HTTP_PORT  GreptimeDB HTTP port (default 14000)
  TMA1_LOG_LEVEL             debug | info | warn | error (default info)
  TMA1_DATA_TTL              Retention for ingested events (default 60d)

Examples:
  tma1-server                                      # start the long-running server
  tma1-server install --adapter claude-code        # wire Claude Code into TMA1
  tma1-server build -- make test                   # ship build output to TMA1
  tma1-server help build                           # see all build flags

See https://tma1.ai for full documentation.
`

const installHelpText = `Usage: tma1-server install [flags]

Install TMA1 into a coding agent. Writes:
  - hook script (~/.tma1/bin/tma1-hook.sh)
  - adapter settings entry (~/.claude/settings.json for claude-code,
    ~/.codex/config.toml for codex) that registers the hook
  - MCP server entry pointing at "tma1-server mcp-serve"
  - embedded skill + slash command files for the adapter
  - when run inside a project: a CLAUDE.md / AGENTS.md instructions
    block and a .gitignore entry for ~/.tma1 data files

Flags:
  --adapter NAME           claude-code | codex (default claude-code)
  --project DIR            Project directory (default: current working
                           directory)
  --skip-project-files     Skip the CLAUDE.md/AGENTS.md block and the
                           .gitignore line. Used by the curl-pipe
                           installer when cwd is unpredictable.
  -n, --dry-run            Print what would change without touching disk
  -h, --help               Show this help

Examples:
  tma1-server install --adapter claude-code
  tma1-server install --adapter codex --project ~/work/myrepo
  tma1-server install --adapter claude-code --dry-run
`

const uninstallHelpText = `Usage: tma1-server uninstall --adapter NAME [flags]

Reverse "tma1-server install" for one adapter: remove the hook script
(when last referenced), the hook registration in the adapter settings,
the MCP entry, embedded skills/commands, and the project instructions
block. The .gitignore line and ~/.tma1/data are left in place unless
--purge-data is passed.

--adapter is required (no default): the asymmetric blast radius of
uninstalling the wrong agent outweighs the convenience of guessing.

Flags:
  --adapter NAME    claude-code | codex (required)
  --project DIR     Project directory (default: current working directory)
  -n, --dry-run     Print what would be removed without touching disk
  --purge-data      Also delete ~/.tma1/data (irreversible)
  -h, --help        Show this help

Examples:
  tma1-server uninstall --adapter claude-code --dry-run
  tma1-server uninstall --adapter codex --purge-data
`

const buildHelpText = `Usage: tma1-server build [flags] [--] <command> [args...]

Wrap <command>, tee its stdout/stderr to your terminal, and ship batched
output to tma1_build_events so agents can query build status through
the perception layer (get_build_status / get_context_bundle).

The "--" separator is optional in most cases — the first non-flag
argument is treated as the start of the wrapped command. Use "--" when
the wrapped command itself begins with a flag, e.g.
"tma1-server build -- --version-check", otherwise that leading flag
would be parsed by tma1-server.

Flags:
  --watch                  Run as a long-running watcher with debounced
                           flushes. Without --watch, the wrapped command
                           runs once and tma1-server exits with its
                           exit code.
  --debounce DURATION      Flush interval for --watch (default 2s)
  --filter-regex PATTERN   Only capture lines matching the regex
  --filter-invert          Invert the regex match (capture non-matching
                           lines instead)
  --tag NAME               Short identifier for this build stream
                           (default: command name)
  --project DIR            Project label override (default: basename of
                           the resolved project root from cwd)
  --no-color               Don't inject FORCE_COLOR / CLICOLOR_FORCE.
                           Default: inject so wrapped tools keep ANSI
                           output even though stdout is captured.
  -h, --help               Show this help

Examples:
  tma1-server build -- make test
  tma1-server build --watch --tag api -- go test ./...
  tma1-server build --filter-regex 'FAIL|PASS' -- npm test
`

const mcpServeHelpText = `Usage: tma1-server mcp-serve

JSON-RPC MCP stdio server. Spawned per session by coding-agent adapters
(Claude Code, Codex, etc.) over stdio; not intended for interactive use.
Talks to the parent tma1-server's GreptimeDB at TMA1_GREPTIMEDB_HTTP_PORT
(default 14000); does not start its own GreptimeDB.

Flags:
  -h, --help    Show this help
`

// The _, _ = discard is intentional: errcheck flags Fprint to an
// io.Writer parameter even though we'd take no useful action on a
// failed write to stdout/stderr.
func printRootHelp(out io.Writer)      { _, _ = fmt.Fprint(out, rootHelpText) }
func printInstallHelp(out io.Writer)   { _, _ = fmt.Fprint(out, installHelpText) }
func printUninstallHelp(out io.Writer) { _, _ = fmt.Fprint(out, uninstallHelpText) }
func printBuildHelp(out io.Writer)     { _, _ = fmt.Fprint(out, buildHelpText) }
func printMCPServeHelp(out io.Writer)  { _, _ = fmt.Fprint(out, mcpServeHelpText) }

// hasHelpFlag reports whether args contains -h or --help before any "--"
// sentinel.
func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

// hasBuildHelpFlag mirrors runBuild's flag boundary: help belongs to
// tma1-server only while we are still parsing known build flags. The first
// unknown token starts the wrapped command, even when it begins with "-".
func hasBuildHelpFlag(args []string) bool {
	for i := 0; i < len(args); {
		switch args[i] {
		case "--":
			return false
		case "-h", "--help":
			return true
		case "--watch", "--filter-invert", "--no-color":
			i++
		case "--debounce", "--filter-regex", "--tag", "--project":
			if i+1 >= len(args) {
				return false
			}
			i += 2
		default:
			return false
		}
	}
	return false
}

// dispatch handles subcommand mode. Caller guarantees len(args) >= 2
// (program name + at least one user-supplied arg). Returns the exit
// code; the caller is responsible for os.Exit.
//
// Stdout/stderr are passed in so tests can capture them. Subcommand
// handlers (runInstall, runBuild, runUninstall, runMCPServe) write
// their own output directly to os.Stdout / os.Stderr — only dispatch's
// own messages (help, version, unknown-subcommand) honour the writers.
// Help requests for subcommands are short-circuited here so they remain
// testable without invoking the underlying handler.
func dispatch(args []string, stdout, stderr io.Writer) int {
	switch args[1] {
	case "-h", "--help", "help":
		return runHelp(args[2:], stdout, stderr)
	case "-v", "--version", "version":
		_, _ = fmt.Fprintln(stdout, "tma1-server", Version)
		return 0
	case "mcp-serve":
		// MCP stdio server. Stdout is reserved for JSON-RPC; logs → stderr.
		if hasHelpFlag(args[2:]) {
			printMCPServeHelp(stdout)
			return 0
		}
		if err := runMCPServe(); err != nil {
			_, _ = fmt.Fprintf(stderr, "mcp-serve: %v\n", err)
			return 1
		}
		return 0
	case "install":
		if hasHelpFlag(args[2:]) {
			printInstallHelp(stdout)
			return 0
		}
		if err := runInstall(args[2:]); err != nil {
			_, _ = fmt.Fprintf(stderr, "install: %v\n", err)
			return 1
		}
		return 0
	case "uninstall":
		if hasHelpFlag(args[2:]) {
			printUninstallHelp(stdout)
			return 0
		}
		if err := runUninstall(args[2:]); err != nil {
			_, _ = fmt.Fprintf(stderr, "uninstall: %v\n", err)
			return 1
		}
		return 0
	case "build":
		if hasBuildHelpFlag(args[2:]) {
			printBuildHelp(stdout)
			return 0
		}
		exitCode, err := runBuild(args[2:])
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "build: %v\n", err)
			return 1
		}
		return exitCode
	default:
		_, _ = fmt.Fprintf(stderr, "tma1-server: unknown subcommand %q\nRun 'tma1-server help' for usage.\n", args[1])
		return 2
	}
}

// runHelp implements `tma1-server help [subcommand]`. Empty topic prints
// the root help; a known topic prints that subcommand's help; an unknown
// topic errors with exit 2.
func runHelp(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printRootHelp(stdout)
		return 0
	}
	switch args[0] {
	case "install":
		printInstallHelp(stdout)
	case "uninstall":
		printUninstallHelp(stdout)
	case "build":
		printBuildHelp(stdout)
	case "mcp-serve":
		printMCPServeHelp(stdout)
	case "help", "version", "-h", "--help", "-v", "--version":
		printRootHelp(stdout)
	default:
		_, _ = fmt.Fprintf(stderr, "tma1-server: unknown help topic %q\nRun 'tma1-server help' for the list of subcommands.\n", args[0])
		return 2
	}
	return 0
}

func main() {
	// Subcommand routing. The default (no args) runs the full HTTP server.
	if len(os.Args) > 1 {
		os.Exit(dispatch(os.Args, os.Stdout, os.Stderr))
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

	// Step 3.4.2: apply versioned ALTER TABLE migrations. Replaces the
	// pre-ledger inline error-tolerant approach (plan risk section
	// calls for a configVersion-style mechanism). Runs after all
	// CREATE TABLE init so migrations can target any tma1_* table.
	if err := greptimedb.RunSchemaMigrations(cfg.GreptimeDBHTTPPort, logger); err != nil {
		logger.Warn("schema migrations failed; subsequent inserts may fail on missing columns", "err", err)
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
	// Wire the live-hook gate: when a Codex session is actively
	// posting hook events to /api/hooks, the JSONL parser skips its
	// own inserts so we don't double-write the same event.
	tw.IsLiveSession = handler.IsCodexSessionLive
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

		// HTTP is done accepting requests, but background INSERTs from
		// the writeq may still be in flight. Drain them before tearing
		// GreptimeDB down so the last few hook/anomaly events aren't
		// lost on shutdown. Bounded so a stuck DB can't block exit.
		if !srv.DrainWrites(2 * time.Second) {
			logger.Warn("writeq drain timed out, some background writes may have been dropped")
		}

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
//	--no-color              don't inject FORCE_COLOR / CLICOLOR_FORCE etc.
//	                        (default: inject so wrapped tools keep ANSI output)
func runBuild(args []string) (int, error) {
	cfg, err := config.Load()
	if err != nil {
		return 1, fmt.Errorf("load config: %w", err)
	}

	// Parse flags up to `--`.
	var (
		watch           bool
		debounceStr     = "2s"
		filterRegex     string
		filterInvert    bool
		tag             string
		projectOverride string
		noColor         bool // default false => ForceColor enabled
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
		case "--no-color":
			noColor = true
			i++
		case "--":
			i++
			goto runCmd
		case "-h", "--help":
			printBuildHelp(os.Stdout)
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
		Project:    project,
		Command:    strings.Join(cmdArgs, " "),
		Tag:        tag,
		Filter:     filter,
		ForceColor: !noColor,
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
	skipProjectFiles := false

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
		case "--skip-project-files":
			// Skip project-local writes (CLAUDE.md / AGENTS.md instructions
			// block and .gitignore). Hooks, MCP, and skills still install
			// globally. Used by install.sh / install.ps1 because the curl-pipe
			// cwd is unpredictable — writing a block to a random directory's
			// CLAUDE.md is worse than not writing it at all. Users wire
			// project-local files later by `cd <project> && tma1-server
			// install --adapter <name>` without this flag.
			skipProjectFiles = true
		case "--dry-run", "-n":
			dryRun = true
		case "-h", "--help":
			printInstallHelp(os.Stdout)
			return nil
		default:
			return fmt.Errorf("unknown flag %q", args[i])
		}
	}

	if skipProjectFiles {
		project = "" // empty triggers the existing ProjectDir != "" guard in both installers
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

	// adapterInstaller is the surface runInstall needs from each
	// per-adapter installer. ClaudeCodeInstaller + CodexInstaller both
	// implement it; both Install() returns the same report shape.
	type adapterInstaller interface {
		Install() (hooks.InstallReport, error)
	}

	var inst adapterInstaller
	switch adapter {
	case "claude-code":
		inst = &hooks.ClaudeCodeInstaller{
			DataDir:            cfg.DataDir,
			Port:               port,
			GreptimeDBHTTPPort: cfg.GreptimeDBHTTPPort,
			ProjectDir:         project,
			Logger:             logger,
			DryRun:             dryRun,
		}
	case "codex":
		inst = &hooks.CodexInstaller{
			DataDir:            cfg.DataDir,
			Port:               port,
			GreptimeDBHTTPPort: cfg.GreptimeDBHTTPPort,
			ProjectDir:         project,
			Logger:             logger,
			DryRun:             dryRun,
		}
	default:
		return fmt.Errorf("adapter %q not supported (available: claude-code, codex)", adapter)
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

// runUninstall reverses runInstall: it removes the hook script, hook
// registrations, MCP server entry, embedded skills/commands, and the
// project-level instruction block written by the matching adapter.
// The `.gitignore` line and `~/.tma1/data/` are left alone unless the
// user passes `--purge-data`.
//
// --adapter is required (no default): the asymmetric blast radius of
// "uninstall the wrong side" outweighs the convenience of a guess.
func runUninstall(args []string) error {
	adapter := ""
	project, _ := os.Getwd()
	dryRun := false
	purgeData := false

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
		case "--purge-data":
			purgeData = true
		case "-h", "--help":
			printUninstallHelp(os.Stdout)
			return nil
		default:
			return fmt.Errorf("unknown flag %q", args[i])
		}
	}
	if adapter == "" {
		return fmt.Errorf("--adapter is required (claude-code|codex)")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	type adapterUninstaller interface {
		Uninstall() (hooks.UninstallReport, error)
	}
	var unin adapterUninstaller
	switch adapter {
	case "claude-code":
		unin = &hooks.ClaudeCodeUninstaller{
			DataDir:    cfg.DataDir,
			ProjectDir: project,
			Logger:     logger,
			DryRun:     dryRun,
			PurgeData:  purgeData,
		}
	case "codex":
		unin = &hooks.CodexUninstaller{
			DataDir:    cfg.DataDir,
			ProjectDir: project,
			Logger:     logger,
			DryRun:     dryRun,
			PurgeData:  purgeData,
		}
	default:
		return fmt.Errorf("adapter %q not supported (available: claude-code, codex)", adapter)
	}

	rep, uninstallErr := unin.Uninstall()

	if dryRun {
		fmt.Printf("TMA1 uninstall report (adapter=%s, DRY-RUN — no files touched)\n", adapter)
	} else {
		fmt.Printf("TMA1 uninstall report (adapter=%s)\n", adapter)
	}
	if rep.HookScript != "" {
		fmt.Printf("  Hook script:   %s\n", rep.HookScript)
	}
	if rep.SettingsPath != "" {
		fmt.Printf("  Settings:      %s\n", rep.SettingsPath)
	}
	if rep.MCPConfigPath != "" {
		fmt.Printf("  MCP config:    %s\n", rep.MCPConfigPath)
	}
	for _, p := range rep.InstructionsPaths {
		fmt.Printf("  Instructions:  %s\n", p)
	}
	if rep.GitignorePath != "" {
		fmt.Printf("  .gitignore:    %s\n", rep.GitignorePath)
	}
	if len(rep.Removed) > 0 {
		header := "  Removed:"
		if dryRun {
			header = "  Would remove:"
		}
		fmt.Println(header)
		for _, r := range rep.Removed {
			fmt.Printf("    - %s\n", r)
		}
	}
	if len(rep.Skipped) > 0 {
		fmt.Println("  Skipped:")
		for _, s := range rep.Skipped {
			fmt.Printf("    - %s\n", s)
		}
	}
	if len(rep.Errors) > 0 {
		fmt.Fprintln(os.Stderr, "  Errors (file left untouched):")
		for _, e := range rep.Errors {
			fmt.Fprintf(os.Stderr, "    - %s\n", e)
		}
	}
	if len(rep.Removed) == 0 && len(rep.Errors) == 0 {
		fmt.Println("  Nothing to remove (already uninstalled or never installed).")
	}

	// Hint about the still-running server. Don't pkill it — that's
	// outside the config-management contract.
	if !dryRun && len(rep.Removed) > 0 {
		fmt.Fprintln(os.Stderr, "  Note: tma1-server may still be running. Stop it with `pkill tma1-server` if you no longer need the dashboard.")
	}

	if uninstallErr != nil {
		return uninstallErr
	}
	if rep.HasErrors() {
		return fmt.Errorf("%d file(s) needed operator review", len(rep.Errors))
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
	// Caller comes from the env var the install adapter wrote into each
	// agent's MCP config (claude_code for CC, codex for Codex, etc).
	// GetPeerSessions uses it to exclude the caller from the empty-
	// agent_source fan-out.
	bundler.Caller = os.Getenv("TMA1_MCP_CALLER")

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
