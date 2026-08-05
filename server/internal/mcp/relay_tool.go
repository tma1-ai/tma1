package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/tma1-ai/tma1/server/internal/strutil"
)

// RelayHandoffTool lets an agent hand off to its peer at a workflow
// milestone. It POSTs to the parent tma1-server's /api/relay/signal,
// which wakes the counterpart terminal (driver↔reviewer).
//
// It runs in the mcp-serve child, which inherits the agent's cwd and the
// env the installer wrote (TMA1_PORT / TMA1_RELAY_ROLE / TMA1_RELAY_TOKEN).
type RelayHandoffTool struct {
	Port   string // parent tma1-server HTTP port (TMA1_PORT)
	Caller string // TMA1_MCP_CALLER: claude_code | codex
	Role   string // TMA1_RELAY_ROLE: driver | reviewer (may be empty → derived from Caller)
	Token  string // TMA1_RELAY_TOKEN: shared secret for /api/relay/signal
	Client *http.Client
}

func (t RelayHandoffTool) Definition() Tool {
	return Tool{
		Name: "tma1_handoff",
		Description: "Hand off to your peer agent at a workflow milestone, waking " +
			"their terminal with an instruction to continue. Stages: " +
			"plan_ready (driver→reviewer), plan_reviewed (reviewer→driver), " +
			"impl_done (driver→reviewer), code_reviewed (reviewer→driver). " +
			"Pass `summary` with your graded findings / conclusion so the peer " +
			"sees it inline and need not re-pull your whole session.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"stage": {
					Type:        "string",
					Description: "Milestone: plan_ready | plan_reviewed | impl_done | code_reviewed.",
				},
				"summary": {
					Type:        "string",
					Description: "Your conclusion / graded findings, shown inline to the peer. Optional but strongly recommended.",
				},
			},
			Required: []string{"stage"},
		},
	}
}

func (t RelayHandoffTool) Call(ctx context.Context, args map[string]any) (CallToolResult, error) {
	stage, _ := args["stage"].(string)
	if stage == "" {
		return errorResult("stage is required (plan_ready | plan_reviewed | impl_done | code_reviewed)"), nil
	}
	summary, _ := args["summary"].(string)
	// Bound the summary so the POST body always stays well under the
	// server's relay-body limit. An oversized summary would otherwise get
	// truncated mid-JSON by the server's LimitReader, fail to parse, and
	// drop the handoff with only a vague "not delivered" to the agent.
	summary = strutil.SafeTruncate(summary, 32<<10)

	role := t.Role
	if role == "" {
		// Derive from the calling agent when the installer didn't set a role.
		if t.Caller == "codex" {
			role = "reviewer"
		} else {
			role = "driver"
		}
	}

	cwd, _ := os.Getwd()
	port := t.Port
	if port == "" {
		port = "14318"
	}

	body, _ := json.Marshal(map[string]string{
		"stage": stage, "role": role, "cwd": cwd, "summary": summary,
	})
	url := fmt.Sprintf("http://127.0.0.1:%s/api/relay/signal", port)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return noteResult("handoff failed: " + err.Error()), nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tma1-Relay-Token", t.Token)

	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		// Don't break the agent loop — surface as a plain (non-error) note.
		return noteResult("handoff not delivered: " + err.Error()), nil
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return noteResult(fmt.Sprintf("handoff not delivered (HTTP %d): %s", resp.StatusCode, string(rb))), nil
	}
	return noteResult("handoff delivered: " + string(rb)), nil
}

// noteResult returns a non-error tool result carrying an informational
// message. The relay handoff intentionally never returns IsError so a
// transient signal failure doesn't interrupt the agent's loop.
func noteResult(msg string) CallToolResult {
	return CallToolResult{Content: []ContentBlock{{Type: "text", Text: msg}}}
}
