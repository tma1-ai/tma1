package perception

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

// Digest captures the SEMANTIC fingerprint of a Bundle — anomalies,
// build/external/project state — but deliberately excludes high-frequency
// counters (duration, tool_calls, tokens) so the digest is stable when
// nothing actionable has changed.
//
// Used by the injection cache to suppress turn-level re-injection of
// identical context (the biggest noise source per the v2 dogfood retro).
type Digest struct {
	Anomalies string `json:"anomalies"`
	Build     string `json:"build"`
	External  string `json:"external"`
	Project   string `json:"project"`
	Focus     string `json:"focus"`
}

// Equal returns true when both digests describe the same actionable state.
// Two bundles producing equal digests can safely be coalesced into one
// injection (the second is noise).
func (d Digest) Equal(other Digest) bool { return d == other }

// DigestDelta marks which Bundle sections differ between two digests.
// Drives the incremental injection path: rather than re-emitting the
// whole bundle every turn (full text => token spend), only sections
// whose content actually changed are rendered. Plan Phase 0.1 spec:
// summary stays under ~500 tokens, and each turn lists only what
// changed since the previous one.
type DigestDelta struct {
	Anomalies bool
	Build     bool
	External  bool
	Project   bool
	Focus     bool
}

// Empty returns true when no section differs. Callers should suppress
// injection entirely on an empty delta — nothing actionable changed.
func (d DigestDelta) Empty() bool {
	return !d.Anomalies && !d.Build && !d.External && !d.Project && !d.Focus
}

// AllSectionsDelta returns a delta with every section flagged. Used on
// first emit for a session and when callers want a forced full render.
func AllSectionsDelta() DigestDelta {
	return DigestDelta{
		Anomalies: true,
		Build:     true,
		External:  true,
		Project:   true,
		Focus:     true,
	}
}

// DiffFrom returns the delta from prev → d.
func (d Digest) DiffFrom(prev Digest) DigestDelta {
	return DigestDelta{
		Anomalies: d.Anomalies != prev.Anomalies,
		Build:     d.Build != prev.Build,
		External:  d.External != prev.External,
		Project:   d.Project != prev.Project,
		Focus:     d.Focus != prev.Focus,
	}
}

// Digest produces a stable fingerprint of bundle b. Identical for two
// bundles that differ only in counters that always change (turn duration,
// tokens, tool call counts).
func (b *Bundle) Digest() Digest {
	if b == nil {
		return Digest{}
	}
	return Digest{
		Anomalies: digestAnomalies(b.Anomalies),
		Build:     digestBuild(b.Build),
		External:  digestExternal(b.External),
		Project:   digestProject(b.ProjectState),
		Focus:     digestFocus(b.Session),
	}
}

func digestAnomalies(items []Anomaly) string {
	if len(items) == 0 {
		return ""
	}
	keys := make([]string, 0, len(items))
	for _, a := range items {
		files := append([]string{}, a.RelatedFiles...)
		sort.Strings(files)
		keys = append(keys, a.Kind+"|"+a.Severity+"|"+strings.Join(files, ","))
	}
	sort.Strings(keys)
	return shortHash(strings.Join(keys, "\n"))
}

func digestBuild(bs *BuildStatus) string {
	if bs == nil {
		return ""
	}
	// Include only fields an agent would care to know changed: tag, exit
	// code, and "is there a new error message?" (hashed). Counters and
	// freshly-updated timestamps don't count as a change.
	exit := "running"
	if bs.LastExitCode != nil {
		exit = "exit=" + strconv.Itoa(*bs.LastExitCode)
	}
	errKey := ""
	if bs.LastErrorMessage != "" {
		errKey = shortHash(bs.LastErrorMessage)
	}
	return shortHash(bs.Tag + "|" + exit + "|" + errKey)
}

func digestExternal(ec *ExternalChanges) string {
	if ec == nil || (ec.HumanCount == 0 && ec.GitCount == 0) {
		return ""
	}
	paths := make([]string, 0, len(ec.HumanChanges))
	for _, c := range ec.HumanChanges {
		paths = append(paths, c.ChangeType+":"+c.FilePath)
	}
	sort.Strings(paths)
	gits := make([]string, 0, len(ec.GitChanges))
	for _, c := range ec.GitChanges {
		gits = append(gits, c.ChangeType+":"+c.GitSHA)
	}
	sort.Strings(gits)
	return shortHash(strings.Join(paths, "\n") + "\n--\n" + strings.Join(gits, "\n"))
}

func digestProject(ps *ProjectState) string {
	if ps == nil {
		return ""
	}
	kf := append([]string{}, ps.KeyFiles...)
	sort.Strings(kf)
	return shortHash(ps.Language + "|" + ps.BuildSystem + "|" + ps.TestFramework + "|" + strings.Join(kf, ","))
}

func digestFocus(s *SessionState) string {
	if s == nil {
		return ""
	}
	// Just the focus file — tool counters and token usage do not count
	// as "context change worth re-injecting".
	return shortHash(s.CurrentFocus)
}

// shortHash returns the first 16 hex chars of sha256(s) — collision risk
// is irrelevant here (the cache is per-session and digest is local).
func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

