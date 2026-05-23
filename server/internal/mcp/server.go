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
	"runtime/debug"
	"strings"
	"sync"
)

const (
	protocolVersion = "2024-11-05"
	serverName      = "tma1"

	scannerInitBuf = 256 * 1024  // 256 KB initial
	scannerMaxBuf  = 4 * 1024 * 1024 // 4 MB max — perception bundles can be larger than devtap's
)

// ServerVersion can be overridden by callers (e.g. main package sets it from build ldflags).
var ServerVersion = "dev"

// ToolHandler implements a single MCP tool. Each tools/call dispatch
// runs in its own goroutine, so individual Call invocations can race
// each other within one Server. Every tma1 Bundler/Detector method
// called from a tool must be safe for concurrent use (they are).
//
// Why concurrent: a single slow or stuck tool call must NOT block
// stdin from being read or other replies from being written. The
// previous serial loop wedged the whole MCP child the first time any
// call took long enough for the agent's stdout pipe to buffer up.
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
	// writeMu serialises writes to s.out so concurrent tool-call
	// goroutines can't interleave JSON frames mid-line. Single mutex
	// is enough — Fprintf is fast vs the SQL roundtrips upstream.
	writeMu sync.Mutex
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
// Each tools/call is dispatched in its own goroutine so a slow tool
// can't block stdin or other replies. Returns when stdin reaches EOF
// or the scanner fails; in-flight tool goroutines are then allowed
// to finish via the WaitGroup so we don't drop their responses.
func (s *Server) Run(ctx context.Context) error {
	scanner := bufio.NewScanner(s.in)
	scanner.Buffer(make([]byte, 0, scannerInitBuf), scannerMaxBuf)

	var inflight sync.WaitGroup
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			inflight.Wait()
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

		// Run each request concurrently. The handler is responsible
		// for shipping exactly one response (or none for
		// notifications); writes are serialised through s.write's
		// mutex so JSON frames can't interleave.
		inflight.Add(1)
		go func(req Request) {
			defer inflight.Done()
			defer func() {
				if r := recover(); r != nil {
					if s.logger != nil {
						s.logger.Error("mcp: recovered panic in tool handler",
							"panic", r,
							"method", req.Method,
							"stack", string(debug.Stack()))
					}
					if req.HasID() {
						s.sendError(req.ID, -32603, "Internal error")
					}
				}
			}()
			s.handle(ctx, req)
		}(req)
	}

	scannerErr := scanner.Err()
	inflight.Wait()
	if scannerErr != nil {
		return fmt.Errorf("mcp: scanner error: %w", scannerErr)
	}
	return nil
}

func (s *Server) handle(ctx context.Context, req Request) {
	// A request without "id" is a notification — no response is ever sent,
	// regardless of method. Side effects still execute (currently none of
	// our methods has any to retain in that case).
	if !req.HasID() {
		return
	}

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
		s.sendError(req.ID, -32601, fmt.Sprintf("Method not found: %s", req.Method))
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
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := fmt.Fprintf(s.out, "%s\n", data); err != nil && s.logger != nil {
		s.logger.Error("write mcp response", "err", err)
	}
}
