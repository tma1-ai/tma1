package relay

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	// staleAfter is a GC backstop for sessions that crashed without
	// sending Stop/SessionEnd. Active sessions keep their LastSeen fresh
	// via Touch on every hook, so a long-running session is never GC'd —
	// this only reaps abandoned entries.
	staleAfter = 12 * time.Hour
	gcInterval = 30 * time.Minute
)

// SignalResult describes what a Signal call did, echoed back to the
// calling agent so it sees confirmation ("woke reviewer via tmux") or a
// reason nothing happened.
type SignalResult struct {
	Stage       string `json:"stage"`
	WokeRole    string `json:"woke_role,omitempty"`
	WakerName   string `json:"waker,omitempty"`
	TargetFound bool   `json:"target_found"`
	Dispatched  bool   `json:"dispatched"`
	Note        string `json:"note,omitempty"`
}

type projectState struct {
	driver   *Target
	reviewer *Target
}

// Coordinator holds per-project driver/reviewer registration and routes
// milestone signals to wake the counterpart's terminal.
type Coordinator struct {
	mu         sync.Mutex
	projects   map[string]*projectState
	waker      *Registry
	projectKey func(string) string // injected (perception.ResolveProjectRoot) to avoid a package dependency
	logger     *slog.Logger
	nextGC     time.Time
}

func NewCoordinator(logger *slog.Logger, waker *Registry, projectKey func(string) string) *Coordinator {
	return &Coordinator{
		projects:   make(map[string]*projectState),
		waker:      waker,
		projectKey: projectKey,
		logger:     logger,
	}
}

func (c *Coordinator) key(cwd string) string {
	if c.projectKey == nil {
		return cwd
	}
	return c.projectKey(cwd)
}

func roleSlot(ps *projectState, role string) **Target {
	switch role {
	case RoleDriver:
		return &ps.driver
	case RoleReviewer:
		return &ps.reviewer
	}
	return nil
}

// Register records (or replaces) the terminal for a role in a project.
// Called on SessionStart.
func (c *Coordinator) Register(t Target) {
	if !ValidRole(t.Role) {
		return
	}
	if t.LastSeen.IsZero() {
		t.LastSeen = time.Now()
	}
	key := c.key(t.CWD)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.upsertLocked(key, t)
	c.gcLocked(time.Now())
}

// Touch refreshes LastSeen for the role's active session so a
// long-running session never ages out. If the target is missing or
// belongs to a different session, it (re)registers from t — covering the
// case where the server started after the agent's SessionStart.
func (c *Coordinator) Touch(t Target) {
	if !ValidRole(t.Role) {
		return
	}
	now := time.Now()
	key := c.key(t.CWD)
	c.mu.Lock()
	defer c.mu.Unlock()
	if ps := c.projects[key]; ps != nil {
		if slot := roleSlot(ps, t.Role); slot != nil && *slot != nil && (*slot).SessionID == t.SessionID {
			(*slot).LastSeen = now
			c.gcLocked(now)
			return
		}
	}
	if t.LastSeen.IsZero() {
		t.LastSeen = now
	}
	c.upsertLocked(key, t)
	c.gcLocked(now)
}

// Unregister removes the role's target on Stop/SessionEnd. It only
// removes when the session id matches (or none given), so a delayed Stop
// from a prior session can't evict a freshly registered one.
func (c *Coordinator) Unregister(cwd, role, sessionID string) {
	if !ValidRole(role) {
		return
	}
	key := c.key(cwd)
	c.mu.Lock()
	defer c.mu.Unlock()
	ps := c.projects[key]
	if ps == nil {
		return
	}
	slot := roleSlot(ps, role)
	if slot == nil || *slot == nil {
		return
	}
	if sessionID != "" && (*slot).SessionID != sessionID {
		return
	}
	*slot = nil
	if ps.driver == nil && ps.reviewer == nil {
		delete(c.projects, key)
	}
}

func (c *Coordinator) upsertLocked(key string, t Target) {
	ps := c.projects[key]
	if ps == nil {
		ps = &projectState{}
		c.projects[key] = ps
	}
	tc := t
	*roleSlot(ps, t.Role) = &tc
}

// Signal looks up the transition for a stage, resolves the counterpart
// target + renders the prompt UNDER the lock, then releases the lock
// before calling the Waker (Wake may exec and block — holding the mutex
// across it would stall all other Register/Touch/Signal calls).
func (c *Coordinator) Signal(ctx context.Context, stage, fromRole, cwd, summary string) (SignalResult, error) {
	res := SignalResult{Stage: stage}
	tr, ok := Lookup(stage)
	if !ok {
		res.Note = "unknown stage"
		return res, nil
	}
	res.WokeRole = tr.WakeRole
	if tr.WakeRole == fromRole {
		res.Note = "transition would wake the sender — misconfigured stage/role"
		return res, nil
	}

	key := c.key(cwd)
	c.mu.Lock()
	var target *Target
	if ps := c.projects[key]; ps != nil {
		if slot := roleSlot(ps, tr.WakeRole); slot != nil && *slot != nil {
			tc := **slot
			target = &tc
		}
	}
	c.mu.Unlock()

	if target == nil {
		res.Note = "no " + tr.WakeRole + " registered for this project"
		return res, nil
	}
	res.TargetFound = true

	if summary == "" {
		summary = "(no summary provided — call get_peer_sessions to read the peer's full output)"
	}
	prompt, err := renderPrompt(tr, promptData{Project: key, FromRole: fromRole, Summary: summary})
	if err != nil {
		res.Note = "render prompt: " + err.Error()
		return res, err
	}

	name, err := c.waker.WakeWith(ctx, *target, prompt)
	if err != nil {
		res.Note = "wake failed: " + err.Error()
		return res, nil
	}
	res.WakerName = name
	res.Dispatched = true
	if name == "worker" {
		// The worker fallback spawns a fresh non-interactive agent; whether
		// its hooks/MCP fire to continue the relay chain is not yet
		// spike-verified, so flag the handoff as best-effort rather than
		// letting dispatched=true imply a guaranteed round-trip.
		res.Note = "worker fallback started; hook/MCP continuation is best-effort (not yet spike-verified)"
	}
	return res, nil
}

// gcLocked reaps abandoned targets. Caller must hold c.mu.
func (c *Coordinator) gcLocked(now time.Time) {
	if now.Before(c.nextGC) {
		return
	}
	cutoff := now.Add(-staleAfter)
	for k, ps := range c.projects {
		if ps.driver != nil && ps.driver.LastSeen.Before(cutoff) {
			ps.driver = nil
		}
		if ps.reviewer != nil && ps.reviewer.LastSeen.Before(cutoff) {
			ps.reviewer = nil
		}
		if ps.driver == nil && ps.reviewer == nil {
			delete(c.projects, k)
		}
	}
	c.nextGC = now.Add(gcInterval)
}
