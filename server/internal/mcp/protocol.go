package mcp

import (
	"bytes"
	"encoding/json"
)

// JSON-RPC 2.0 + MCP message types.

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`

	hasID bool `json:"-"`
}

// UnmarshalJSON decodes the request and remembers whether an "id" field
// was present in the wire payload. JSON-RPC 2.0 distinguishes a request
// (id present) from a notification (id absent); the default decoder into
// `ID any` loses that signal because an omitted field produces ID == nil
// the same way an explicit `"id": null` does.
//
// Per the spec, `null` is a discouraged but legal id value: clients that
// send it still expect a response, with the response echoing id:null.
// Only an OMITTED id field marks the message as a notification (no
// response). We therefore set hasID=true whenever the field is present,
// regardless of value, so the handler knows to reply.
//
// Implementation note: Go's json.Unmarshal collapses `"id": null` and an
// omitted "id" into the same nil zero-value for both `any` and
// `*json.RawMessage` fields — only a key-level inspection on a
// map[string]json.RawMessage can tell them apart.
func (r *Request) UnmarshalJSON(data []byte) error {
	// Reset so a caller reusing a Request value doesn't carry hasID/ID
	// from a previous decode.
	*r = Request{}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, ok := fields["jsonrpc"]; ok {
		if err := json.Unmarshal(raw, &r.JSONRPC); err != nil {
			return err
		}
	}
	if raw, ok := fields["method"]; ok {
		if err := json.Unmarshal(raw, &r.Method); err != nil {
			return err
		}
	}
	if raw, ok := fields["params"]; ok {
		r.Params = raw
	}
	if raw, ok := fields["id"]; ok {
		r.hasID = true
		// Decode the id value only when it's not the JSON literal `null`;
		// leaving r.ID == nil for null preserves the spec's
		// String|Number|Null shape on the response side.
		if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			if err := json.Unmarshal(raw, &r.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// HasID reports whether the request carried an "id" field at all,
// including the case where its value was JSON null. Per JSON-RPC 2.0
// only an omitted id marks a message as a notification; an explicit
// id:null still expects a response (echoing id:null). Use this — not
// `req.ID != nil` — to decide whether to call sendResult / sendError.
func (r *Request) HasID() bool { return r.hasID }

type Response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type InitializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    Capabilities `json:"capabilities"`
	ServerInfo      ServerInfo   `json:"serverInfo"`
}

type Capabilities struct {
	Tools *ToolsCapability `json:"tools,omitempty"`
}

type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

type InputSchema struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties,omitempty"`
	Required   []string            `json:"required,omitempty"`
}

type Property struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type ToolsListResult struct {
	Tools []Tool `json:"tools"`
}

type CallToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type CallToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
