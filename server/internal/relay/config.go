package relay

import (
	"encoding/json"
	"fmt"
	"os"
	"text/template"
)

// transitionConfig is the on-disk shape of an optional relay.json that
// overrides the built-in transition table. Absent file → built-in
// defaults; a malformed file is reported so the caller can fall back.
type transitionConfig struct {
	Transitions map[string]struct {
		WakeRole string `json:"wake_role"`
		Prompt   string `json:"prompt"`
	} `json:"transitions"`
}

// DefaultTransitions returns a copy of the built-in transition table so a
// Coordinator can own (and optionally replace) its map without mutating
// package state.
func DefaultTransitions() map[string]Transition {
	out := make(map[string]Transition, len(transitions))
	for k, v := range transitions {
		out[k] = v
	}
	return out
}

// LoadTransitions reads an override table from path. The caller should
// treat os.IsNotExist specially (use DefaultTransitions). Every prompt is
// parsed as a template up front so a typo fails loudly at load instead of
// at the first handoff.
func LoadTransitions(path string) (map[string]Transition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg transitionConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(cfg.Transitions) == 0 {
		return nil, fmt.Errorf("%s: no transitions defined", path)
	}
	// Merge overrides ONTO the built-in defaults so a partial relay.json
	// (e.g. customising only plan_ready) keeps the other stages working
	// instead of dropping them from the table.
	out := DefaultTransitions()
	for stage, t := range cfg.Transitions {
		if !ValidRole(t.WakeRole) {
			return nil, fmt.Errorf("stage %q: invalid wake_role %q", stage, t.WakeRole)
		}
		tmpl, err := template.New(stage).Parse(t.Prompt)
		if err != nil {
			return nil, fmt.Errorf("stage %q: %w", stage, err)
		}
		out[stage] = Transition{WakeRole: t.WakeRole, promptTmpl: tmpl}
	}
	return out, nil
}
