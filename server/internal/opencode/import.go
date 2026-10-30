// Package opencode imports the official OpenCode v1 session export format.
package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tma1-ai/tma1/server/internal/sqlutil"
)

var ErrInvalidSnapshot = errors.New("invalid OpenCode snapshot")

type Snapshot struct {
	Info struct {
		ID        string `json:"id"`
		Directory string `json:"directory"`
		Time      struct {
			Created int64 `json:"created"`
		} `json:"time"`
	} `json:"info"`
	Messages []struct {
		Info struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
			Role      string `json:"role"`
			ModelID   string `json:"modelID"`
			Time      struct {
				Created   int64 `json:"created"`
				Completed int64 `json:"completed"`
			} `json:"time"`
			Tokens *struct {
				Input     *int64 `json:"input"`
				Output    *int64 `json:"output"`
				Reasoning *int64 `json:"reasoning"`
				Cache     struct {
					Read  *int64 `json:"read"`
					Write *int64 `json:"write"`
				} `json:"cache"`
			} `json:"tokens"`
		} `json:"info"`
		Parts []struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
			MessageID string `json:"messageID"`
			Type      string `json:"type"`
			Time      struct {
				Start int64 `json:"start"`
			} `json:"time"`
			Text      string `json:"text"`
			Synthetic bool   `json:"synthetic"`
			Ignored   bool   `json:"ignored"`
			CallID    string `json:"callID"`
			Tool      string `json:"tool"`
			State     struct {
				Status string          `json:"status"`
				Input  json.RawMessage `json:"input"`
				Output string          `json:"output"`
				Error  string          `json:"error"`
				Time   struct {
					Start int64 `json:"start"`
					End   int64 `json:"end"`
				} `json:"time"`
			} `json:"state"`
		} `json:"parts"`
	} `json:"messages"`
}

type record struct{ table, id, columns, values string }

var sourceID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func records(snapshot Snapshot) ([]record, error) {
	if !sourceID.MatchString(snapshot.Info.ID) || snapshot.Info.Time.Created <= 0 {
		return nil, fmt.Errorf("invalid session id or creation time")
	}
	sid := "oc:" + snapshot.Info.ID
	q := func(s string) string { return "'" + sqlutil.Escape(s) + "'" }
	stamp := func(ms int64) string { return q(time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)) }
	var out []record
	seen := map[string]bool{}
	add := func(table, id, columns, values string) error {
		key := table + ":" + id
		if seen[key] {
			return fmt.Errorf("duplicate source record %s", id)
		}
		seen[key] = true
		out = append(out, record{table, id, "source_record_id,session_id," + columns, q(id) + "," + q(sid) + "," + values})
		return nil
	}
	event := func(id string, ts int64, kind, tool, input, result, call string, success string) error {
		return add("tma1_hook_events", id, "ts,agent_source,event_type,cwd,tool_name,tool_input,tool_result,tool_use_id,tool_success", strings.Join([]string{stamp(ts), "'opencode'", q(kind), q(snapshot.Info.Directory), q(tool), q(input), q(result), q(call), success}, ","))
	}
	if err := event("start", snapshot.Info.Time.Created, "SessionStart", "", "", "", "", "NULL"); err != nil {
		return nil, err
	}
	for _, msg := range snapshot.Messages {
		m := msg.Info
		if !sourceID.MatchString(m.ID) || m.SessionID != snapshot.Info.ID || m.Time.Created <= 0 || (m.Role != "user" && m.Role != "assistant") {
			return nil, fmt.Errorf("invalid message identity, role or timestamp")
		}
		// Assistant snapshots can still be changing until completion. Step-finish
		// tokens duplicate message totals, so only the completed message owns usage.
		if m.Role == "assistant" && m.Time.Completed == 0 {
			continue
		}
		if m.Role == "assistant" && m.Time.Completed < m.Time.Created {
			return nil, fmt.Errorf("invalid message completion time")
		}
		if m.Role == "assistant" && m.Tokens != nil {
			t := m.Tokens
			for _, n := range []*int64{t.Input, t.Output, t.Reasoning, t.Cache.Read, t.Cache.Write} {
				if n == nil || *n < 0 || *n > 1_000_000_000 {
					return nil, fmt.Errorf("invalid token count")
				}
			}
			// OpenCode subtracts reasoning from output in Session.getUsage. TMA1's
			// output_tokens includes reasoning; cache reads/writes remain additive.
			values := fmt.Sprintf("%s,'assistant','usage',%s,%d,%d,%d,%d,%d,%d", stamp(m.Time.Created), q(m.ModelID), *t.Input, *t.Output+*t.Reasoning, *t.Reasoning, *t.Cache.Read, *t.Cache.Write, m.Time.Completed-m.Time.Created)
			if err := add("tma1_messages", m.ID+":usage", `ts,"role",message_type,model,input_tokens,output_tokens,reasoning_tokens,cache_read_tokens,cache_creation_tokens,duration_ms`, values); err != nil {
				return nil, err
			}
		}
		for _, p := range msg.Parts {
			if !sourceID.MatchString(p.ID) || p.SessionID != snapshot.Info.ID || p.MessageID != m.ID {
				return nil, fmt.Errorf("invalid part identity")
			}
			switch p.Type {
			case "text", "reasoning":
				if p.Ignored || p.Synthetic || p.Text == "" {
					continue
				}
				kind := m.Role
				if p.Type == "reasoning" {
					kind = "thinking"
				}
				partTime := m.Time.Created
				if p.Time.Start > 0 {
					partTime = p.Time.Start
				}
				values := strings.Join([]string{stamp(partTime), q(m.Role), q(kind), q(p.Text), q(m.ModelID)}, ",")
				if err := add("tma1_messages", p.ID, `ts,"role",message_type,content,model`, values); err != nil {
					return nil, err
				}
				if kind == "user" {
					if err := event(p.ID, m.Time.Created, "UserPromptSubmit", "", "", p.Text, "", "NULL"); err != nil {
						return nil, err
					}
				}
			case "tool":
				st := p.State
				if st.Status != "completed" && st.Status != "error" {
					continue
				}
				if st.Time.Start <= 0 || st.Time.End < st.Time.Start || p.CallID == "" || p.Tool == "" {
					return nil, fmt.Errorf("invalid completed tool")
				}
				if err := event(p.ID+":start", st.Time.Start, "PreToolUse", p.Tool, string(st.Input), "", p.CallID, "NULL"); err != nil {
					return nil, err
				}
				result, kind := st.Output, "PostToolUse"
				if st.Status == "error" {
					result, kind = st.Error, "PostToolUseFailure"
				}
				if err := event(p.ID+":end", st.Time.End, kind, p.Tool, "", result, p.CallID, strconv.FormatBool(st.Status == "completed")); err != nil {
					return nil, err
				}
			}
		}
		if m.Role == "assistant" {
			if err := event(m.ID+":stop", m.Time.Completed, "Stop", "", "", "", "", "NULL"); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// Importer serializes snapshots so overlapping idle notifications and retries
// cannot both append a record after observing it as absent.
type Importer struct {
	mu    sync.Mutex
	query func(context.Context, string) ([]byte, error)
}

func New(query func(context.Context, string) ([]byte, error)) *Importer {
	return &Importer{query: query}
}

func (i *Importer) Import(ctx context.Context, snapshot Snapshot) (int, error) {
	data, err := records(snapshot)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrInvalidSnapshot, err)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	inserted := 0
	for _, table := range []string{"tma1_messages", "tma1_hook_events"} {
		existing := map[string]bool{}
		// Bound each result below the SQL client's response cap for long sessions.
		for offset := 0; ; offset += 1000 {
			body, err := i.query(ctx, "SELECT source_record_id FROM "+table+" WHERE session_id='"+sqlutil.Escape("oc:"+snapshot.Info.ID)+"' AND source_record_id IS NOT NULL ORDER BY source_record_id LIMIT 1000 OFFSET "+strconv.Itoa(offset))
			if err != nil {
				return inserted, fmt.Errorf("read imported records: %w", err)
			}
			var response struct {
				Output []struct {
					Records struct {
						Rows [][]string `json:"rows"`
					} `json:"records"`
				} `json:"output"`
			}
			if err := json.Unmarshal(body, &response); err != nil {
				return inserted, fmt.Errorf("decode imported records: %w", err)
			}
			if len(response.Output) != 1 {
				return inserted, fmt.Errorf("missing imported record query result")
			}
			for _, row := range response.Output[0].Records.Rows {
				if len(row) != 1 {
					return inserted, fmt.Errorf("invalid imported record row")
				}
				existing[row[0]] = true
			}
			if len(response.Output[0].Records.Rows) < 1000 {
				break
			}
		}

		for _, r := range data {
			if r.table != table || existing[r.id] {
				continue
			}
			if _, err := i.query(ctx, "INSERT INTO "+r.table+" ("+r.columns+") VALUES ("+r.values+")"); err != nil {
				return inserted, fmt.Errorf("write imported record: %w", err)
			}
			inserted++
		}
	}
	return inserted, nil
}
