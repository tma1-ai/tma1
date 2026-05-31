package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tma1-ai/tma1/server/internal/pathutil"
	"github.com/tma1-ai/tma1/server/internal/perception"
)

// ContextBundleTool returns a perception bundle for the current project.
//
// MCP runs as a child process spawned by Claude Code (one process per
// session). It inherits cwd from the parent, so we derive the project from
// os.Getwd() rather than asking the agent for it.
type ContextBundleTool struct {
	Bundler *perception.Bundler
}

func (t ContextBundleTool) Definition() Tool {
	return Tool{
		Name: "get_context_bundle",
		Description: "Return the TMA1 perception bundle for the current project: " +
			"session state (tool history, tokens, current focus, recent files). " +
			"Call this at the start of a turn — or whenever you need to recover " +
			"context lost to compaction — before continuing your work.",
		InputSchema: InputSchema{
			Type:       "object",
			Properties: map[string]Property{},
		},
	}
}

func (t ContextBundleTool) Call(ctx context.Context, _ map[string]any) (CallToolResult, error) {
	if t.Bundler == nil {
		return errorResult("bundler not configured"), nil
	}
	cwd, _ := os.Getwd()
	bundle := t.Bundler.BuildBundle(ctx, "", cwd)
	out, err := bundle.RenderJSON()
	if err != nil {
		return CallToolResult{}, fmt.Errorf("render bundle: %w", err)
	}
	return CallToolResult{
		Content: []ContentBlock{{Type: "text", Text: out}},
	}, nil
}

// SessionStateTool returns the state of a specific session (or the active
// session for the current cwd if session_id is omitted).
type SessionStateTool struct {
	Bundler *perception.Bundler
}

func (t SessionStateTool) Definition() Tool {
	return Tool{
		Name: "get_session_state",
		Description: "Get the current (or specified) session's tool history, token " +
			"usage, and recent focus files. Use this when you need to recall what " +
			"you've already done in this session. Set verbose=true to also receive " +
			"a chronological list of recent tool calls.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"session_id": {
					Type:        "string",
					Description: "Optional session ID. Defaults to the active session for the current cwd.",
				},
				"verbose": {
					Type:        "boolean",
					Description: "When true, the response includes an `actions` array with the most recent PreToolUse / PostToolUse / PostToolUseFailure entries (default cap: 50).",
				},
				"action_limit": {
					Type:        "integer",
					Description: "Optional cap on the verbose action list. 1-200, default 50. Ignored when verbose is false.",
				},
			},
		},
	}
}

func (t SessionStateTool) Call(ctx context.Context, args map[string]any) (CallToolResult, error) {
	if t.Bundler == nil {
		return errorResult("bundler not configured"), nil
	}

	sessionID, _ := args["session_id"].(string)
	cwd, _ := os.Getwd()

	if sessionID == "" {
		found, err := t.Bundler.LatestSessionForCWD(ctx, cwd)
		if err != nil {
			return errorResult(fmt.Sprintf("resolve session: %v", err)), nil
		}
		sessionID = found
	}

	if sessionID == "" {
		// No observed session for this cwd. Return an empty payload rather
		// than an error so the agent can fall back gracefully.
		out, _ := json.MarshalIndent(map[string]any{
			"session": nil,
			"note":    "no active session observed for current cwd",
		}, "", "  ")
		return CallToolResult{Content: []ContentBlock{{Type: "text", Text: string(out)}}}, nil
	}

	state, err := t.Bundler.GetSessionState(ctx, sessionID)
	if err != nil {
		return errorResult(fmt.Sprintf("get session state: %v", err)), nil
	}

	payload := map[string]any{"session": state}

	// verbose=true => include raw action list. This is what the plan
	// originally proposed as a separate get_recent_actions tool;
	// folding it into get_session_state keeps the tool surface small.
	// Failure to fetch actions doesn't fail the call; the agent still
	// gets state.
	if verbose, _ := args["verbose"].(bool); verbose {
		limit := intArg(args, "action_limit", 50)
		actions, err := t.Bundler.GetRecentActions(ctx, sessionID, limit)
		if err != nil {
			payload["actions_error"] = err.Error()
		} else {
			payload["actions"] = actions
			payload["action_count"] = len(actions)
		}
	}

	out, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return CallToolResult{}, fmt.Errorf("marshal session: %w", err)
	}
	return CallToolResult{Content: []ContentBlock{{Type: "text", Text: string(out)}}}, nil
}

// PeerSessionsTool returns recent session content from peer coding
// agents that worked on the same project as the caller. "Peer" is
// relative to whichever agent invoked the MCP tool: CC sees Codex /
// OpenClaw / Copilot CLI, Codex sees CC / OpenClaw / Copilot CLI, and
// so on (the Bundler's Caller field excludes self). Lets one agent
// act on review feedback or work left by another without manual copy-
// paste between terminals.
type PeerSessionsTool struct {
	Bundler *perception.Bundler
}

func (t PeerSessionsTool) Definition() Tool {
	return Tool{
		Name: "get_peer_sessions",
		Description: "Pull recent session content from peer coding agents " +
			"(Claude Code, Codex, OpenClaw, Copilot CLI) that worked on the " +
			"same project. Use this when the user asks you to act on feedback " +
			"or work left by another agent, or invokes `/tma1-peer`. Filters " +
			"by agent_source; empty string returns all peers excluding the " +
			"caller (the agent invoking this MCP tool).",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"agent_source": {
					Type:        "string",
					Description: "Peer agent: claude_code / codex / openclaw / copilot_cli. Aliases accepted: cc | claude → claude_code, copilot → copilot_cli. Empty = all peers except the caller (top N per agent).",
				},
				"project": {
					Type: "string",
					Description: "Override the auto-derived project. Absolute path " +
						"means \"sessions whose cwd is under this root\". A bare " +
						"name falls back to legacy basename matching. Empty = " +
						"derive from the caller's cwd.",
				},
				"limit": {
					Type:        "integer",
					Description: "Max sessions per agent (1-5, default 1). When agent_source is empty, applied per peer agent, not globally.",
				},
				"message_limit": {
					Type:        "integer",
					Description: "Max messages per session (1-100, default 20).",
				},
				"since_min": {
					Type:        "integer",
					Description: "How many minutes back to look (default 1440 = 24h).",
				},
			},
		},
	}
}

func (t PeerSessionsTool) Call(ctx context.Context, args map[string]any) (CallToolResult, error) {
	if t.Bundler == nil {
		return errorResult("bundler not configured"), nil
	}

	// The overall fan-out deadline (cwd lookup → session list → per-session
	// enrichment, across peers) is applied once at the MCP dispatch boundary
	// (toolCallTimeout in server.go), so every tool is bounded uniformly.

	agent, _ := args["agent_source"].(string)
	limit := intArg(args, "limit", 1)
	msgLimit := intArg(args, "message_limit", 20)
	sinceMin := intArg(args, "since_min", 24*60)

	// Project scoping: caller override > absolute root from cwd. Using the
	// absolute root (not just the basename) means two projects named "server"
	// don't show each other's sessions.
	project, _ := args["project"].(string)
	if strings.TrimSpace(project) == "" {
		cwd, _ := os.Getwd()
		project = perception.ResolveProjectRoot(cwd)
	}

	sessions, partialFailures, err := t.Bundler.GetPeerSessions(ctx, agent, project, limit, msgLimit, sinceMin)
	if err != nil {
		return errorResult(fmt.Sprintf("get peer sessions: %v", err)), nil
	}
	payload := map[string]any{
		"project":      project,
		"agent_filter": agent,
		"count":        len(sessions),
		"sessions":     sessions,
	}
	if len(sessions) > 0 {
		// Most-recent first (already sorted by SQL ORDER BY last_ms DESC).
		// Surface the freshest session's age at the top so the agent can
		// decide quickly whether the peer work is current.
		payload["most_recent_session"] = map[string]any{
			"agent_source":      sessions[0].AgentSource,
			"session_id":        sessions[0].SessionID,
			"last_activity":     sessions[0].LastActivityAt,
			"last_activity_ago": sessions[0].LastActivityAgo,
		}
	} else {
		payload["note"] = "no peer sessions found for this project in the time window"
	}
	// Surface per-agent failures from the all-peers fan-out. The skill
	// instructs the agent to check this before treating empty sessions
	// as "no activity" — a non-empty map means the result is incomplete.
	if len(partialFailures) > 0 {
		payload["partial_failures"] = partialFailures
	}
	out, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return CallToolResult{}, fmt.Errorf("marshal peer sessions: %w", err)
	}
	return CallToolResult{Content: []ContentBlock{{Type: "text", Text: string(out)}}}, nil
}

// intArg pulls an integer from MCP args. JSON-RPC delivers numbers as
// float64; accept that plus int / int64 just in case.
func intArg(args map[string]any, key string, fallback int) int {
	v, ok := args[key]
	if !ok {
		return fallback
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return fallback
}

// ProjectStateTool returns the latest indexed snapshot of the current
// project's structure: language, build/test system, key files, top-level
// directories. Useful at SessionStart so the agent doesn't have to ls/cat
// its way around an unfamiliar repo.
type ProjectStateTool struct {
	Bundler *perception.Bundler
}

func (t ProjectStateTool) Definition() Tool {
	return Tool{
		Name: "get_project_state",
		Description: "Return the indexed structure of the current project: " +
			"primary language, build system, test framework, key files (README, " +
			"CLAUDE.md, etc.), and top-level directories. Call this when you " +
			"start work in an unfamiliar project to avoid re-discovering it.",
		InputSchema: InputSchema{
			Type:       "object",
			Properties: map[string]Property{},
		},
	}
}

func (t ProjectStateTool) Call(ctx context.Context, _ map[string]any) (CallToolResult, error) {
	if t.Bundler == nil {
		return errorResult("bundler not configured"), nil
	}
	cwd, _ := os.Getwd()
	project := projectFromCwd(cwd)
	if project == "" {
		out, _ := json.MarshalIndent(map[string]any{
			"project": "",
			"note":    "cannot resolve project from cwd",
		}, "", "  ")
		return CallToolResult{Content: []ContentBlock{{Type: "text", Text: string(out)}}}, nil
	}
	state, err := t.Bundler.GetProjectState(ctx, project)
	if err != nil {
		return errorResult(fmt.Sprintf("get project state: %v", err)), nil
	}
	if state == nil {
		out, _ := json.MarshalIndent(map[string]any{
			"project": project,
			"note":    "project not yet indexed — make any agent hook fire and try again",
		}, "", "  ")
		return CallToolResult{Content: []ContentBlock{{Type: "text", Text: string(out)}}}, nil
	}
	out, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return CallToolResult{}, fmt.Errorf("marshal project state: %w", err)
	}
	return CallToolResult{Content: []ContentBlock{{Type: "text", Text: string(out)}}}, nil
}

// ExternalChangesTool returns recent human-attributed file modifications
// and git activity in the current project — what changed while the agent
// wasn't looking. Default window: last 30 minutes; override via `since_min`.
type ExternalChangesTool struct {
	Bundler *perception.Bundler
}

func (t ExternalChangesTool) Definition() Tool {
	return Tool{
		Name: "get_external_changes",
		Description: "List files modified by a human + git commits / branch " +
			"switches that happened in the current project recently. Use this " +
			"before continuing work after a context refresh to learn what " +
			"changed outside of your edits.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"since_min": {
					Type:        "integer",
					Description: "How many minutes back to look (default 30).",
				},
			},
		},
	}
}

func (t ExternalChangesTool) Call(ctx context.Context, args map[string]any) (CallToolResult, error) {
	if t.Bundler == nil {
		return errorResult("bundler not configured"), nil
	}
	cwd, _ := os.Getwd()
	project := projectFromCwd(cwd)
	if project == "" {
		out, _ := json.MarshalIndent(map[string]any{
			"project": "",
			"note":    "cannot resolve project from cwd",
		}, "", "  ")
		return CallToolResult{Content: []ContentBlock{{Type: "text", Text: string(out)}}}, nil
	}

	sinceMin := intArg(args, "since_min", 30)
	if sinceMin <= 0 {
		sinceMin = 30
	}
	since := time.Now().Add(-time.Duration(sinceMin) * time.Minute)

	changes, err := t.Bundler.GetExternalChanges(ctx, project, since)
	if changes == nil {
		if err != nil {
			return errorResult(fmt.Sprintf("get external changes: %v", err)), nil
		}
		out, _ := json.MarshalIndent(map[string]any{
			"project":   project,
			"since_min": sinceMin,
			"note":      "no external changes observed in the window",
		}, "", "  ")
		return CallToolResult{Content: []ContentBlock{{Type: "text", Text: string(out)}}}, nil
	}
	// Partial result: when one of the two underlying queries failed but
	// the other returned rows, GetExternalChanges returns both. Attach
	// the error message as a top-level partial_error field so the
	// success shape (project / human_changes / git_changes / counts at
	// the top level) stays stable for downstream consumers.
	if err != nil {
		changes.PartialError = err.Error()
	}
	out, mErr := json.MarshalIndent(changes, "", "  ")
	if mErr != nil {
		return CallToolResult{}, fmt.Errorf("marshal external changes: %w", mErr)
	}
	return CallToolResult{Content: []ContentBlock{{Type: "text", Text: string(out)}}}, nil
}

// BuildStatusTool returns the current build status for the project the
// agent is working in (cwd-derived). It's useful when the agent needs to
// know whether a watched build is currently failing before suggesting
// further edits.
type BuildStatusTool struct {
	Bundler *perception.Bundler
}

func (t BuildStatusTool) Definition() Tool {
	return Tool{
		Name: "get_build_status",
		Description: "Get the most recent build/dev output captured by " +
			"`tma1-server build -- <cmd>` for the current project: last exit code, " +
			"error count in the last 30 minutes, last error message. Call this " +
			"after suggesting edits to verify whether the build is now green.",
		InputSchema: InputSchema{
			Type:       "object",
			Properties: map[string]Property{},
		},
	}
}

func (t BuildStatusTool) Call(ctx context.Context, _ map[string]any) (CallToolResult, error) {
	if t.Bundler == nil {
		return errorResult("bundler not configured"), nil
	}
	cwd, _ := os.Getwd()
	project := projectFromCwd(cwd)
	if project == "" {
		out, _ := json.MarshalIndent(map[string]any{
			"project": "",
			"note":    "cannot resolve project from cwd",
		}, "", "  ")
		return CallToolResult{Content: []ContentBlock{{Type: "text", Text: string(out)}}}, nil
	}
	status, err := t.Bundler.GetBuildStatus(ctx, project)
	if err != nil {
		return errorResult(fmt.Sprintf("get build status: %v", err)), nil
	}
	if status == nil {
		out, _ := json.MarshalIndent(map[string]any{
			"project": project,
			"note":    "no build events recorded for this project in the last 24 hours",
		}, "", "  ")
		return CallToolResult{Content: []ContentBlock{{Type: "text", Text: string(out)}}}, nil
	}
	out, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return CallToolResult{}, fmt.Errorf("marshal build status: %w", err)
	}
	return CallToolResult{Content: []ContentBlock{{Type: "text", Text: string(out)}}}, nil
}

// projectFromCwd mirrors perception.BuildBundle's project derivation:
// basename of ResolveProjectRoot(cwd). pathutil.Basename handles both
// POSIX and Windows separators so MCP responds the same for agents on
// either OS.
func projectFromCwd(cwd string) string {
	root := perception.ResolveProjectRoot(cwd)
	if root == "" {
		return ""
	}
	return pathutil.Basename(root)
}

// AnomaliesTool exposes the perception Detector directly so an agent can
// drill into the list of active anomalies without re-fetching the full
// bundle. Returned data is the same struct included in get_context_bundle.
type AnomaliesTool struct {
	Bundler *perception.Bundler
}

func (t AnomaliesTool) Definition() Tool {
	return Tool{
		Name: "get_anomalies",
		Description: "List anomalies tma1 has detected for a session — looping " +
			"file edits, repeated failed builds, over-long sessions. Use this to " +
			"decide whether to change approach before continuing. Defaults to the " +
			"active session for the current cwd.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"session_id": {
					Type:        "string",
					Description: "Optional session ID. Defaults to the active session for the current cwd.",
				},
			},
		},
	}
}

func (t AnomaliesTool) Call(ctx context.Context, args map[string]any) (CallToolResult, error) {
	if t.Bundler == nil {
		return errorResult("bundler not configured"), nil
	}

	sessionID, _ := args["session_id"].(string)
	cwd, _ := os.Getwd()

	if sessionID == "" {
		found, err := t.Bundler.LatestSessionForCWD(ctx, cwd)
		if err != nil {
			return errorResult(fmt.Sprintf("resolve session: %v", err)), nil
		}
		sessionID = found
	}

	if sessionID == "" {
		out, _ := json.MarshalIndent(map[string]any{
			"anomalies": []perception.Anomaly{},
			"note":      "no active session observed for current cwd",
		}, "", "  ")
		return CallToolResult{Content: []ContentBlock{{Type: "text", Text: string(out)}}}, nil
	}

	// IMPORTANT: pull-channel callers MUST use DetectPreview, not Detect.
	// Detect advances the per-rule suppression window (LastEmittedAt = now)
	// and INSERTs into tma1_anomaly_emits — both side effects silently
	// weaken the next push-channel Stop block. DetectPreview runs the
	// same rules + resolvers but never writes state.
	anomalies, err := t.Bundler.DetectPreview(ctx, sessionID)
	if err != nil {
		return errorResult(fmt.Sprintf("preview anomalies: %v", err)), nil
	}
	payload := map[string]any{
		"session_id": sessionID,
		"anomalies":  anomalies,
		"count":      len(anomalies),
	}
	out, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return CallToolResult{}, fmt.Errorf("marshal anomalies: %w", err)
	}
	return CallToolResult{Content: []ContentBlock{{Type: "text", Text: string(out)}}}, nil
}

func errorResult(msg string) CallToolResult {
	return CallToolResult{
		Content: []ContentBlock{{Type: "text", Text: msg}},
		IsError: true,
	}
}
