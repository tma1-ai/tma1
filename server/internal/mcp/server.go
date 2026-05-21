// Package mcp implements a JSON-RPC 2.0 stdio server speaking the MCP protocol.
//
// IMPORTANT: stdout is reserved for JSON-RPC frames. Any log output must go to
// stderr. Callers must redirect global loggers (log, slog) to os.Stderr before
// invoking Run; otherwise the client will fail to parse the JSON-RPC stream.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

const (
	protocolVersion = "2024-11-05"
	serverName      = "tma1"

	scannerInitBuf = 256 * 1024  // 256 KB initial
	scannerMaxBuf  = 4 * 1024 * 1024 // 4 MB max — perception bundles can be larger than devtap's
)

// ServerVersion can be overridden by callers (e.g. main package sets it from build ldflags).
var ServerVersion = "dev"

// ToolHandler implements a single MCP tool. Implementations must be safe for
// concurrent use because clients may pipeline requests.
type ToolHandler interface {
	Definition() Tool
	Call(ctx context.Context, args map[string]any) (CallToolResult, error)
}

// Server runs the MCP stdio loop.
type Server struct {
	tools  map[string]ToolHandler
	logger *slog.Logger
	in     io.Reader
	out    io.Writer
}

// NewServer creates a Server with the given tools.
// logger MUST write to stderr (not stdout) — see package doc.
func NewServer(logger *slog.Logger, tools ...ToolHandler) *Server {
	m := make(map[string]ToolHandler, len(tools))
	for _, t := range tools {
		m[t.Definition().Name] = t
	}
	return &Server{
		tools:  m,
		logger: logger,
		in:     os.Stdin,
		out:    os.Stdout,
	}
}

// SetIO overrides the input/output streams (used in tests).
func (s *Server) SetIO(in io.Reader, out io.Writer) {
	s.in = in
	s.out = out
}

// Run reads JSON-RPC frames from s.in and writes responses to s.out.
// Returns when stdin reaches EOF or the scanner fails.
func (s *Server) Run(ctx context.Context) error {
	scanner := bufio.NewScanner(s.in)
	scanner.Buffer(make([]byte, 0, scannerInitBuf), scannerMaxBuf)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var req Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.sendError(nil, -32700, "Parse error")
			continue
		}

		s.handle(ctx, req)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("mcp: scanner error: %w", err)
	}
	return nil
}

func (s *Server) handle(ctx context.Context, req Request) {
	switch req.Method {
	case "initialize":
		s.sendResult(req.ID, InitializeResult{
			ProtocolVersion: protocolVersion,
			Capabilities: Capabilities{
				Tools: &ToolsCapability{},
			},
			ServerInfo: ServerInfo{
				Name:    serverName,
				Version: ServerVersion,
			},
		})

	case "notifications/initialized":
		// notifications never get a response

	case "tools/list":
		defs := make([]Tool, 0, len(s.tools))
		for _, t := range s.tools {
			defs = append(defs, t.Definition())
		}
		s.sendResult(req.ID, ToolsListResult{Tools: defs})

	case "tools/call":
		var params CallToolParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			s.sendError(req.ID, -32602, "Invalid params")
			return
		}
		t, ok := s.tools[params.Name]
		if !ok {
			s.sendResult(req.ID, CallToolResult{
				Content: []ContentBlock{{Type: "text", Text: fmt.Sprintf("Unknown tool: %s", params.Name)}},
				IsError: true,
			})
			return
		}
		result, err := t.Call(ctx, params.Arguments)
		if err != nil {
			s.sendResult(req.ID, CallToolResult{
				Content: []ContentBlock{{Type: "text", Text: fmt.Sprintf("Tool error: %v", err)}},
				IsError: true,
			})
			return
		}
		s.sendResult(req.ID, result)

	case "ping":
		s.sendResult(req.ID, map[string]any{})

	default:
		if req.ID != nil {
			s.sendError(req.ID, -32601, fmt.Sprintf("Method not found: %s", req.Method))
		}
	}
}

func (s *Server) sendResult(id any, result any) {
	s.write(Response{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *Server) sendError(id any, code int, message string) {
	s.write(Response{JSONRPC: "2.0", ID: id, Error: &Error{Code: code, Message: message}})
}

func (s *Server) write(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("marshal mcp response", "err", err)
		}
		return
	}
	if _, err := fmt.Fprintf(s.out, "%s\n", data); err != nil && s.logger != nil {
		s.logger.Error("write mcp response", "err", err)
	}
}
