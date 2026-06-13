package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
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

// pendingWake records that we woke a role and are waiting for it to hand
// back (its own next signal). It powers busy-debounce (don't re-wake a
// role we're still waiting on) and the wake-timeout (nudge the originator
// if the peer never responds).
type pendingWake struct {
	id         uint64 // identifies this reservation across cancel/arm races
	originator string // the role to nudge on timeout
	stage      string
	since      time.Time
	timer      *time.Timer
}

// pendingSeq tags each reservation so cancel/arm act only on their own.
var pendingSeq atomic.Uint64

// Coordinator holds per-project driver/reviewer registration and routes
// milestone signals to wake the counterpart's terminal.
type Coordinator struct {
	mu          sync.Mutex
	projects    map[string]*projectState
	pending     map[string]*pendingWake // key = pendKey(project, wokeRole)
	waker       *Registry
	transitions map[string]Transition
	wakeTimeout time.Duration // 0 disables the timeout nudge
	stopped     bool
	projectKey  func(string) string // injected (perception.ResolveProjectRoot) to avoid a package dependency
	logger      *slog.Logger
	nextGC      time.Time
}

func NewCoordinator(logger *slog.Logger, waker *Registry, projectKey func(string) string) *Coordinator {
	return &Coordinator{
		projects:    make(map[string]*projectState),
		pending:     make(map[string]*pendingWake),
		waker:       waker,
		transitions: DefaultTransitions(),
		projectKey:  projectKey,
		logger:      logger,
	}
}

// SetTransitions replaces the transition table (e.g. loaded from
// relay.json). Call before the Coordinator starts handling signals.
func (c *Coordinator) SetTransitions(t map[string]Transition) {
	if len(t) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.transitions = t
}

// SetWakeTimeout sets how long to wait for a woken peer to hand back
// before nudging the originator. Zero disables the nudge.
func (c *Coordinator) SetWakeTimeout(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wakeTimeout = d
}

// KnownStage reports whether the (possibly overridden) transition table
// has this stage. The HTTP handler uses it so validation matches what
// Signal will actually accept, instead of the package-level default table.
func (c *Coordinator) KnownStage(stage string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.transitions[stage]
	return ok
}

// ValidStages returns the sorted stages the live table accepts.
func (c *Coordinator) ValidStages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.transitions))
	for k := range c.transitions {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func pendKey(project, role string) string { return project + "|" + role }

// clearPendingLocked cancels and removes a role's pending wake. Caller
// must hold c.mu.
func (c *Coordinator) clearPendingLocked(project, role string) {
	k := pendKey(project, role)
	if pw := c.pending[k]; pw != nil {
		if pw.timer != nil {
			pw.timer.Stop()
		}
		delete(c.pending, k)
	}
}

// Stop cancels all outstanding timeout timers. Called on server shutdown.
func (c *Coordinator) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
	for k, pw := range c.pending {
		if pw.timer != nil {
			pw.timer.Stop()
		}
		delete(c.pending, k)
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
	// A brand-new session for this role means whatever we previously woke is
	// gone — clear any stale pending so it doesn't block future handoffs.
	// This matters when the prior agent crashed without SessionEnd and the
	// wake-timeout is disabled (otherwise the timeout would self-heal).
	if ps := c.projects[key]; ps != nil {
		if slot := roleSlot(ps, t.Role); slot != nil && *slot != nil && (*slot).SessionID != t.SessionID {
			c.clearPendingLocked(key, t.Role)
		}
	}
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
	c.clearPendingLocked(key, role)
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
	key := c.key(cwd)

	c.mu.Lock()
	tr, ok := c.transitions[stage]
	if !ok {
		c.mu.Unlock()
		res.Note = "unknown stage"
		return res, nil
	}
	res.WokeRole = tr.WakeRole
	if tr.WakeRole == fromRole {
		c.mu.Unlock()
		res.Note = "transition would wake the sender — misconfigured stage/role"
		return res, nil
	}

	// The sender handing off means it finished the task it was woken for —
	// clear any pending wake we were tracking for it (and its timeout).
	c.clearPendingLocked(key, fromRole)

	// Busy-debounce: if we already woke the target role and it hasn't
	// handed back yet, don't re-wake it — drop honestly and tell the
	// caller to retry once the peer is free.
	if pw := c.pending[pendKey(key, tr.WakeRole)]; pw != nil {
		c.mu.Unlock()
		res.Note = fmt.Sprintf("dropped: %s busy since %s (stage %s) — re-call tma1_handoff once it's free",
			tr.WakeRole, pw.since.Format(time.Kitchen), pw.stage)
		return res, nil
	}

	var target *Target
	if ps := c.projects[key]; ps != nil {
		if slot := roleSlot(ps, tr.WakeRole); slot != nil && *slot != nil {
			tc := **slot
			target = &tc
		}
	}
	if target == nil {
		c.mu.Unlock()
		res.Note = "no " + tr.WakeRole + " registered for this project"
		return res, nil
	}
	res.TargetFound = true

	// Reserve the pending slot UNDER the lock, before releasing it to Wake.
	// Without this, two concurrent signals would both pass the busy check
	// above and double-wake the target (or spawn two workers). The reserved
	// entry has no timer yet — the timer is armed only after a successful
	// Wake (armReservation), and the reservation is cancelled if Wake fails.
	resID := pendingSeq.Add(1)
	c.pending[pendKey(key, tr.WakeRole)] = &pendingWake{
		id: resID, originator: fromRole, stage: stage, since: time.Now(),
	}
	c.mu.Unlock()

	if summary == "" {
		summary = "(no summary provided — call get_peer_sessions to read the peer's full output)"
	}
	prompt, err := renderPrompt(tr, promptData{Project: key, FromRole: fromRole, Summary: summary})
	if err != nil {
		c.cancelReservation(key, tr.WakeRole, resID)
		res.Note = "render prompt: " + err.Error()
		return res, err
	}

	name, err := c.waker.WakeWith(ctx, *target, prompt)
	if err != nil {
		c.cancelReservation(key, tr.WakeRole, resID)
		if errors.Is(err, errTargetBusy) {
			res.Note = "dropped: " + tr.WakeRole + " terminal busy — re-call tma1_handoff once it's free"
		} else {
			res.Note = "wake failed: " + err.Error()
		}
		return res, nil
	}
	res.WakerName = name
	res.Dispatched = true
	c.armReservation(key, tr.WakeRole, resID)
	if name == "worker" {
		// The worker fallback spawns a fresh non-interactive agent; whether
		// its hooks/MCP fire to continue the relay chain is not yet
		// spike-verified, so flag the handoff as best-effort rather than
		// letting dispatched=true imply a guaranteed round-trip.
		res.Note = "worker fallback started; hook/MCP continuation is best-effort (not yet spike-verified)"
	}
	return res, nil
}

// cancelReservation removes a pending reservation iff it's still the one we
// made (id match), so a failed Wake doesn't evict a newer reservation.
func (c *Coordinator) cancelReservation(project, role string, id uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := pendKey(project, role)
	if pw := c.pending[k]; pw != nil && pw.id == id {
		if pw.timer != nil {
			pw.timer.Stop()
		}
		delete(c.pending, k)
	}
}

// armReservation attaches the timeout timer to our reservation after a
// successful Wake — but only if it's still present and unchanged (a very
// fast hand-back could have already cleared it). No-op after Stop.
func (c *Coordinator) armReservation(project, role string, id uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return
	}
	pw := c.pending[pendKey(project, role)]
	if pw == nil || pw.id != id || pw.timer != nil {
		return
	}
	if c.wakeTimeout > 0 {
		pw.timer = time.AfterFunc(c.wakeTimeout, func() {
			c.onWakeTimeout(project, role)
		})
	}
}

// onWakeTimeout fires when a woken peer hasn't handed back in time. It
// nudges the originator's terminal. The lock is released before calling
// the Waker (Wake may block).
func (c *Coordinator) onWakeTimeout(project, wokeRole string) {
	c.mu.Lock()
	k := pendKey(project, wokeRole)
	pw := c.pending[k]
	if pw == nil { // already cleared by a hand-back / unregister
		c.mu.Unlock()
		return
	}
	delete(c.pending, k)
	stage := pw.stage
	originator := pw.originator
	timeout := c.wakeTimeout // read mutex-guarded field under the lock
	var target *Target
	if ps := c.projects[project]; ps != nil {
		if slot := roleSlot(ps, originator); slot != nil && *slot != nil {
			tc := **slot
			target = &tc
		}
	}
	c.mu.Unlock()

	if target == nil {
		return
	}
	prompt := fmt.Sprintf("No response from the %s on your %q handoff after %s. "+
		"Check on the peer terminal or follow up manually.", wokeRole, stage, timeout)
	if _, err := c.waker.WakeWith(context.Background(), *target, prompt); err != nil && c.logger != nil {
		c.logger.Warn("relay wake-timeout nudge failed", "project", project, "role", originator, "err", err)
	}
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
			c.clearPendingLocked(k, RoleDriver)
		}
		if ps.reviewer != nil && ps.reviewer.LastSeen.Before(cutoff) {
			ps.reviewer = nil
			c.clearPendingLocked(k, RoleReviewer)
		}
		if ps.driver == nil && ps.reviewer == nil {
			delete(c.projects, k)
		}
	}
	c.nextGC = now.Add(gcInterval)
}
