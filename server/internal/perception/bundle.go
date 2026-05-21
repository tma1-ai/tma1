package perception

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/tma1-ai/tma1/server/internal/pathutil"
)

// Bundle is the complete perception snapshot returned to agents.
// Phase 0.1: session. Phase 0.3 adds anomalies. Phase 1.1 adds build.
// Phase 1.2 adds external changes. Phase 1.3 adds project state.
type Bundle struct {
	Project      string           `json:"project,omitempty"`
	GeneratedAt  time.Time        `json:"generated_at"`
	Session      *SessionState    `json:"session,omitempty"`
	Anomalies    []Anomaly        `json:"anomalies,omitempty"`
	Build        *BuildStatus     `json:"build,omitempty"`
	External     *ExternalChanges `json:"external,omitempty"`
	ProjectState *ProjectState    `json:"project_state,omitempty"`
}

// Bundler builds Bundles. Safe for concurrent use.
type Bundler struct {
	client   *Client
	detector *Detector
	logger   *slog.Logger
}

// NewBundler returns a Bundler querying GreptimeDB on localhost:<httpPort>.
// The bundler owns its own Detector (the Detector also queries GreptimeDB,
// but with its own connection pool + per-session cache).
func NewBundler(httpPort int, logger *slog.Logger) *Bundler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Bundler{
		client:   NewClient(httpPort),
		detector: NewDetector(httpPort, logger),
		logger:   logger,
	}
}

// Detector returns the bundler's anomaly detector. The handler layer needs
// it directly to invalidate the cache when new hook events arrive.
func (b *Bundler) Detector() *Detector { return b.detector }

// BuildBundle assembles a bundle for the given session_id and/or cwd.
//
// Resolution order:
//  1. If sessionID is non-empty, use it directly.
//  2. Otherwise look up the most recent session for cwd.
//
// If neither resolves to a session, returns an empty bundle (no error). This
// is the fail-safe path used when an agent runs in a project we've never
// observed before.
func (b *Bundler) BuildBundle(ctx context.Context, sessionID, cwd string) *Bundle {
	bundle := &Bundle{
		GeneratedAt: time.Now().UTC(),
		Project:     projectName(cwd),
	}

	if sessionID == "" && cwd != "" {
		if found, err := b.LatestSessionForCWD(ctx, cwd); err == nil && found != "" {
			sessionID = found
		} else if err != nil {
			b.logger.Debug("perception: LatestSessionForCWD failed", "err", err)
		}
	}

	if sessionID != "" {
		state, err := b.GetSessionState(ctx, sessionID)
		if err != nil {
			b.logger.Debug("perception: GetSessionState failed", "err", err, "session", sessionID)
		}
		bundle.Session = state

		// UserPromptSubmit only sees anomalies routed to its channel. Stop-
		// block anomalies stay out of the bundle so the same finding isn't
		// shown via two injection points (Stop block + next prompt prepend).
		// PostToolUse-channel anomalies are emitted inline by the hook
		// handler at the moment they fire, not next turn.
		bundle.Anomalies = b.detector.DetectByChannel(ctx, sessionID, ChannelUserPromptSubmit)
	}

	// Build status is project-scoped (not session-scoped) — populated even
	// when no agent session is observed, so a developer running just the
	// `tma1-server build` watcher can still query status.
	if bundle.Project != "" {
		status, err := b.GetBuildStatus(ctx, bundle.Project)
		if err != nil {
			b.logger.Debug("perception: GetBuildStatus failed", "err", err, "project", bundle.Project)
		}
		bundle.Build = status

		// External changes: human-attributed file changes + git activity
		// in the last 30 minutes (the default window). Agent-attributed
		// changes are filtered out by GetExternalChanges itself.
		ext, err := b.GetExternalChanges(ctx, bundle.Project, time.Now().Add(-30*time.Minute))
		if err != nil {
			b.logger.Debug("perception: GetExternalChanges failed", "err", err, "project", bundle.Project)
		}
		bundle.External = ext

		// Project state (language, build/test system, key files). Static
		// snapshot; the project sensor refreshes it lazily on each hook
		// event, gated by IndexTTL.
		ps, err := b.GetProjectState(ctx, bundle.Project)
		if err != nil {
			b.logger.Debug("perception: GetProjectState failed", "err", err, "project", bundle.Project)
		}
		bundle.ProjectState = ps
	}

	return bundle
}

// projectName extracts a short, stable project label from a cwd path. It
// resolves to the project root (git root → marker file → cwd) and returns
// that root's basename. This guarantees that running an agent in any
// subdirectory of the same repo produces the same project label — which
// matters because `tma1-server build` writes events tagged with the same
// derived label, and the bundler must use the same value to query them
// back.
//
// "/Users/dennis/programming/go/tma1/server" → "tma1"
// "/Users/dennis/programming/go/tma1"        → "tma1"
func projectName(cwd string) string {
	root := ResolveProjectRoot(strings.TrimRight(cwd, `/\`))
	if root == "" {
		return ""
	}
	return pathutil.Basename(root)
}

// RenderSummary returns a full <tma1-context> markdown summary suitable
// for hook injection (no incremental gating). Equivalent to
// RenderSummaryDelta(AllSectionsDelta()).
//
// Bounded to about 500 tokens / ~2 KB by truncating list lengths.
// Returns an empty string when no section produces content — so we
// never inject pure noise.
func (b *Bundle) RenderSummary() string {
	return b.RenderSummaryDelta(AllSectionsDelta())
}

// RenderSummaryDelta renders only the bundle sections marked in delta.
// This is the incremental injection path the plan calls for: only
// list what changed since the previous turn. When only the anomaly
// set changed, only the anomalies block ships -- counters + project
// orientation already in the agent's context don't get re-emitted.
//
// Returns an empty string when delta is empty OR when no included
// section actually has content to render.
func (b *Bundle) RenderSummaryDelta(delta DigestDelta) string {
	if b == nil || delta.Empty() {
		return ""
	}
	var sections strings.Builder

	if delta.Project {
		renderProjectStateLine(&sections, b.ProjectState)
	}
	if delta.Focus {
		renderSessionBlock(&sections, b.Session)
	}
	if delta.Build {
		renderBuildBlock(&sections, b.Build)
	}
	if delta.External {
		renderExternalBlock(&sections, b.External)
	}
	if delta.Anomalies {
		renderAnomaliesBlock(&sections, b.Anomalies)
	}

	if sections.Len() == 0 {
		return ""
	}

	var out strings.Builder
	out.Grow(sections.Len() + 64)
	out.WriteString("<tma1-context>\n")
	if b.Project != "" {
		fmt.Fprintf(&out, "project: %s\n", b.Project)
	}
	out.WriteString(sections.String())
	out.WriteString("</tma1-context>")
	return out.String()
}

// renderProjectStateLine — one terse row identifying language + build/test
// system. Full project structure is available via get_project_state.
func renderProjectStateLine(sb *strings.Builder, ps *ProjectState) {
	if ps == nil || ps.Language == "" {
		return
	}
	row := "stack: " + ps.Language
	if ps.BuildSystem != "" {
		row += " · build=" + ps.BuildSystem
	}
	if ps.TestFramework != "" {
		row += " · test=" + ps.TestFramework
	}
	fmt.Fprintln(sb, row)
}

// renderSessionBlock — session header (id/duration/counters/focus/tools).
// All counter fields are excluded from the digest so this block only
// re-emits when Focus changes — see digestFocus.
func renderSessionBlock(sb *strings.Builder, s *SessionState) {
	if s == nil {
		return
	}
	fmt.Fprintf(sb, "session: %s\n", abbrev(s.SessionID, 8))
	if s.DurationMinutes > 0 {
		fmt.Fprintf(sb, "duration: %d min\n", s.DurationMinutes)
	}
	if s.ToolCallCount > 0 {
		fmt.Fprintf(sb, "tool_calls: %d\n", s.ToolCallCount)
	}
	if s.TokensInput+s.TokensOutput > 0 {
		fmt.Fprintf(sb, "tokens: in=%d out=%d\n", s.TokensInput, s.TokensOutput)
	}
	if s.CurrentFocus != "" {
		fmt.Fprintf(sb, "current_focus: %s\n", shortPath(s.CurrentFocus))
	}
	if len(s.RecentTools) > 0 {
		parts := make([]string, 0, 6)
		for i, t := range s.RecentTools {
			if i >= 6 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s×%d", t.Name, t.Count))
		}
		fmt.Fprintf(sb, "tools: %s\n", strings.Join(parts, ", "))
	}
	if len(s.RecentFiles) > 0 {
		short := make([]string, 0, 5)
		for i, p := range s.RecentFiles {
			if i >= 5 {
				break
			}
			short = append(short, shortPath(p))
		}
		fmt.Fprintf(sb, "recent_files: %s\n", strings.Join(short, ", "))
	}
}

// renderBuildBlock — current build status. Surfaces the most recent
// stderr/error line whenever one exists; agents need the message
// itself, not "errors=2" counts.
func renderBuildBlock(sb *strings.Builder, bs *BuildStatus) {
	if bs == nil {
		return
	}
	switch {
	case bs.LastExitCode != nil && *bs.LastExitCode != 0:
		fmt.Fprintf(sb, "build: ❌ %s exit=%d\n", bs.Tag, *bs.LastExitCode)
	case bs.LastExitCode != nil:
		fmt.Fprintf(sb, "build: ✅ %s exit=0\n", bs.Tag)
	case bs.LastEventAt.IsZero():
		// no recent activity
	default:
		fmt.Fprintf(sb, "build: %s (running)\n", bs.Tag)
	}
	if bs.ErrorsInLast30Min > 0 {
		fmt.Fprintf(sb, "build_errors_30m: %d\n", bs.ErrorsInLast30Min)
	}
	if last := bs.LastErrorMessage; last != "" {
		age := relativeAge(bs.LastErrorAt)
		recovered := !bs.LastErrorAt.IsZero() && bs.LastEventAt.After(bs.LastErrorAt.Add(10*time.Second))
		tag := age
		if recovered {
			tag += ", may have recovered"
		}
		fmt.Fprintf(sb, "build_last_error (%s): %s\n", tag, oneLine(last, 200))
	}
}

// renderExternalBlock — human-attributed file changes + most recent git
// event since the bundle's window.
func renderExternalBlock(sb *strings.Builder, ext *ExternalChanges) {
	if ext == nil || (ext.HumanCount == 0 && ext.GitCount == 0) {
		return
	}
	if ext.HumanCount > 0 {
		fmt.Fprintf(sb, "external_human_changes: %d\n", ext.HumanCount)
		short := make([]string, 0, 3)
		for i, c := range ext.HumanChanges {
			if i >= 3 {
				break
			}
			short = append(short, shortPath(c.FilePath))
		}
		if len(short) > 0 {
			fmt.Fprintf(sb, "external_files: %s\n", strings.Join(short, ", "))
		}
	}
	if ext.GitCount > 0 {
		c := ext.GitChanges[0]
		if c.GitMessage != "" {
			fmt.Fprintf(sb, "external_git: %s — %s\n", c.ChangeType, c.GitMessage)
		} else {
			fmt.Fprintf(sb, "external_git: %s\n", c.ChangeType)
		}
	}
}

// renderAnomaliesBlock — bottom of the tag so the agent reads them last
// (and most recent in memory).
func renderAnomaliesBlock(sb *strings.Builder, anomalies []Anomaly) {
	if len(anomalies) == 0 {
		return
	}
	sb.WriteString("anomalies:\n")
	for i, a := range anomalies {
		if i >= 4 { // cap to keep injection size bounded
			fmt.Fprintf(sb, "  ... +%d more\n", len(anomalies)-4)
			break
		}
		fmt.Fprintf(sb, "  - [%s] %s — %s\n", strings.ToUpper(a.Severity), a.Kind, a.Suggestion)
	}
}

// RenderJSON returns the bundle as indented JSON, suitable for MCP responses.
func (b *Bundle) RenderJSON() (string, error) {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func abbrev(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// relativeAge formats a past time as "Xs/Xm/Xh ago", or "just now" when
// < 5s, or "" when t is zero. Compact enough for an inline bundle row.
func relativeAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// oneLine flattens multi-line text into a single line (newlines → " · "),
// collapses runs of whitespace, and truncates to maxLen bytes. Used to put
// a multi-line build error into a single bundle row.
func oneLine(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", " · ")
	// Single-pass whitespace collapse: emit each rune unless the
	// previous one was already a space. Replaces the prior O(n²)
	// `for strings.Contains(s, "  ")` loop.
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	out := strings.TrimSpace(b.String())
	if len(out) > maxLen {
		out = out[:maxLen] + "…"
	}
	return out
}

// shortPath collapses long absolute paths to "…/parent/file" for terser
// output. Splits on both '/' and '\' so a Windows agent's path renders
// the same as a POSIX one.
func shortPath(p string) string {
	const maxLen = 60
	if len(p) <= maxLen {
		return p
	}
	parts := pathutil.Split(p)
	if len(parts) <= 3 {
		return p
	}
	tail := strings.Join(parts[len(parts)-3:], "/")
	return ".../" + tail
}
