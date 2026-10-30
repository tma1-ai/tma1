// Package derive lifts the ingest-time derivation logic that
// extracts `tool_file_path`, `tool_command_prefix`, `tool_success`,
// and `tool_error_summary` from a tool event payload. Both the CC
// hook handler (server/internal/handler/hooks.go) and the transcript
// parsers (server/internal/transcript/{codex,copilot_cli}.go)
// call this so the four derived columns get populated regardless of
// the agent source.
//
// Without this, only CC events filled those columns and downstream
// queries (anomaly rules, peer enrichment, recent-files) fell back
// to `COALESCE(tool_file_path, regexp_match(tool_input, '...'))` —
// a per-row regex scan whose cost grows linearly with row count.
package derive

import (
	"regexp"
	"strings"

	"github.com/tma1-ai/tma1/server/internal/strutil"
)

// fileInputRE / commandInputRE extract the most common tool_input
// fields. Regex (not json.Unmarshal) because tool_input is often
// truncated at ingest — a best-effort lift of the leading fields is
// more robust than all-or-nothing JSON parsing.
var (
	fileInputRE    = regexp.MustCompile(`"file_path"\s*:\s*"([^"]+)"`)
	commandInputRE = regexp.MustCompile(`"command"\s*:\s*"((?:[^"\\]|\\.)*)"`)
)

// Fields lifts file_path / command_prefix / success / error_summary
// from a tool event so downstream queries can WHERE on those
// columns directly instead of running regex over tool_input.
//
// Returns ("", "", nil, "") for events that don't have these
// signals (SessionStart, Notification, …) — caller writes NULL.
//
// Parameter shape uses primitives so the package stays free of
// the handler / transcript types. Callers compose the relevant
// fields from their own payload structs.
func Fields(eventName, toolName, toolInput, toolResult, message string) (filePath, cmdPrefix string, success *bool, errSummary string) {
	// File path: any tool that takes a file_path arg
	// (Edit / Write / Read / MultiEdit on CC; apply_patch etc. on
	// other agents).
	if m := fileInputRE.FindStringSubmatch(toolInput); len(m) >= 2 {
		filePath = m[1]
	}

	// Command prefix: leading 200 chars of Bash / shell-tool commands.
	// `Bash` is CC; `exec_command` is Codex; Copilot may
	// use either name — match by lowercase substring so the helper
	// is tolerant.
	if isShellTool(toolName) {
		if m := commandInputRE.FindStringSubmatch(toolInput); len(m) >= 2 {
			cmdPrefix = strutil.SafeTruncate(unescapeJSONString(m[1]), 200)
		}
	}

	// Success / error_summary: PostToolUse / PostToolUseFailure tell
	// us directly via the event name. Result body is the error text
	// for failures.
	switch eventName {
	case "PostToolUse":
		t := true
		success = &t
	case "PostToolUseFailure":
		f := false
		success = &f
		errSummary = strutil.SafeTruncate(firstNonEmpty(toolResult, message), 400)
	}
	return
}

// isShellTool reports whether toolName looks like a shell-invoking
// tool whose tool_input carries a `"command":"…"` field worth
// extracting. Lowercase substring match so callers don't have to
// normalise.
func isShellTool(toolName string) bool {
	low := strings.ToLower(toolName)
	return low == "bash" || low == "exec_command" || low == "shell" || strings.HasPrefix(low, "shell.")
}

// unescapeJSONString reverses the common JSON string escapes so
// command_prefix stored in the DB is human-readable. Unknown
// escapes pass through unchanged.
func unescapeJSONString(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			next := s[i+1]
			switch next {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '"', '\\', '/':
				b.WriteByte(next)
			default:
				b.WriteByte(c)
				b.WriteByte(next)
			}
			i++
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
