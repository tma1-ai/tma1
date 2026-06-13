package relay

import (
	"sort"
	"strings"
	"text/template"
)

// Milestone stages that drive the handoff state machine.
const (
	StagePlanReady    = "plan_ready"
	StagePlanReviewed = "plan_reviewed"
	StageImplDone     = "impl_done"
	StageCodeReviewed = "code_reviewed"
)

// Transition encodes, for a milestone stage, which role to wake and the
// instruction prompt to deliver to that role's terminal.
type Transition struct {
	WakeRole   string
	promptTmpl *template.Template
}

// promptData is the template context for a wake prompt.
type promptData struct {
	Project  string
	FromRole string
	Summary  string
}

// Prompt text. The reviewer-side prompts deliberately script the
// high-signal review motion (pull → read real code → graded findings →
// revision order → hand back) so an auto-triggered review matches the
// quality of a hand-driven one. The driver-side prompts inline the
// peer's summary so the driver needn't re-pull the full session.
const (
	planReadyPrompt = "A peer agent (the driver) just finished a PLAN for project {{.Project}} and handed it off for review.\n\n" +
		"Do a real review, not a skim:\n" +
		"1. Pull the full plan — call get_peer_sessions (agent_source=claude_code) or read the plan file directly.\n" +
		"2. READ the actual code the plan touches before judging — do not review from the plan text alone.\n" +
		"3. Report findings graded P1 (blocking) / P2 (should-fix), each with file:line references.\n" +
		"4. End with a minimal revision order.\n" +
		"5. When done, call tma1_handoff with stage=\"plan_reviewed\" and summary set to your graded findings."

	planReviewedPrompt = "The reviewer finished reviewing your plan. Their findings:\n\n{{.Summary}}\n\n" +
		"Apply the P1 items, decide on each P2, update the plan, then continue."

	implDonePrompt = "A peer agent (the driver) finished an IMPLEMENTATION for project {{.Project}}.\n\n" +
		"1. Pull the latest work — get_peer_sessions (agent_source=claude_code) or inspect the diff (git diff).\n" +
		"2. Run your code-review skill against the REAL changes, not the description.\n" +
		"3. Report findings graded P1/P2 with file:line references, then a minimal fix order.\n" +
		"4. When done, call tma1_handoff with stage=\"code_reviewed\" and summary set to your review report."

	codeReviewedPrompt = "The reviewer finished reviewing your implementation. Their report:\n\n{{.Summary}}\n\n" +
		"Apply the P1 fixes, decide on each P2, then run your own review pass before finishing."
)

// transitions is the built-in, hardcoded state machine for Phase 1.
// Made configurable in a later phase.
var transitions = map[string]Transition{
	StagePlanReady:    {WakeRole: RoleReviewer, promptTmpl: mustTmpl(planReadyPrompt)},
	StagePlanReviewed: {WakeRole: RoleDriver, promptTmpl: mustTmpl(planReviewedPrompt)},
	StageImplDone:     {WakeRole: RoleReviewer, promptTmpl: mustTmpl(implDonePrompt)},
	StageCodeReviewed: {WakeRole: RoleDriver, promptTmpl: mustTmpl(codeReviewedPrompt)},
}

func mustTmpl(s string) *template.Template {
	return template.Must(template.New("relay_prompt").Parse(s))
}

// Lookup returns the transition for a stage, false if the stage is unknown.
func Lookup(stage string) (Transition, bool) {
	t, ok := transitions[stage]
	return t, ok
}

// ValidStages returns all known stage names, sorted, for validation
// errors and the MCP tool description.
func ValidStages() []string {
	out := make([]string, 0, len(transitions))
	for k := range transitions {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func renderPrompt(t Transition, d promptData) (string, error) {
	var b strings.Builder
	if err := t.promptTmpl.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}
