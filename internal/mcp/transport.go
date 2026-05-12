package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
)

const JSONRPCVersion = "2.0"

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      any             `json:"id,omitempty"`
}

type Response struct {
	JSONRPC string         `json:"jsonrpc"`
	Result  any            `json:"result,omitempty"`
	Error   *ResponseError `json:"error,omitempty"`
	ID      any            `json:"id,omitempty"`
}

type ResponseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type InitializeParams struct {
	ProtocolVersion string   `json:"protocolVersion"`
	Capabilities    struct{} `json:"capabilities"`
	ClientInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"clientInfo"`
}

type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

type ServerCapabilities struct {
	Tools *struct{} `json:"tools,omitempty"`
}

type ToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type Transport struct {
	server   *Server
	repoRoot string
	mu       sync.Mutex
}

func NewTransport(repoRoot string) *Transport {
	return &Transport{
		server:   New(repoRoot),
		repoRoot: repoRoot,
	}
}

func (t *Transport) Run(ctx context.Context) error {
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var req Request
		if err := decoder.Decode(&req); err != nil {
			if err == io.EOF {
				return nil
			}
			slog.Error("MCP: decode error", "error", err)
			continue
		}

		resp := t.handleRequest(ctx, req)
		if resp == nil {
			continue
		}

		t.mu.Lock()
		if err := encoder.Encode(resp); err != nil {
			slog.Error("MCP: encode error", "error", err)
		}
		t.mu.Unlock()
	}
}

func (t *Transport) handleRequest(ctx context.Context, req Request) *Response {
	switch req.Method {
	case "initialize":
		var params InitializeParams
		if req.Params != nil {
			json.Unmarshal(req.Params, &params)
		}

		result := InitializeResult{
			ProtocolVersion: "2024-11-05",
			Capabilities:    ServerCapabilities{},
			ServerInfo: struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			}{
				Name:    "majordomo",
				Version: "0.1.0",
			},
		}
		result.Capabilities.Tools = &struct{}{}

		return &Response{
			JSONRPC: JSONRPCVersion,
			Result:  result,
			ID:      req.ID,
		}

	case "tools/list":
		return &Response{
			JSONRPC: JSONRPCVersion,
			Result: map[string]any{
				"tools": t.server.ListTools(),
			},
			ID: req.ID,
		}

	case "tools/call":
		var params ToolCallParams
		if req.Params != nil {
			json.Unmarshal(req.Params, &params)
		}

		result, err := t.server.CallTool(ctx, params.Name, params.Arguments)
		if err != nil {
			return &Response{
				JSONRPC: JSONRPCVersion,
				Error: &ResponseError{
					Code:    -32603,
					Message: err.Error(),
				},
				ID: req.ID,
			}
		}

		return &Response{
			JSONRPC: JSONRPCVersion,
			Result:  result,
			ID:      req.ID,
		}

	case "ping":
		return &Response{
			JSONRPC: JSONRPCVersion,
			Result:  map[string]any{"pong": true},
			ID:      req.ID,
		}

	default:
		return &Response{
			JSONRPC: JSONRPCVersion,
			Error: &ResponseError{
				Code:    -32601,
				Message: fmt.Sprintf("method not found: %s", req.Method),
			},
			ID: req.ID,
		}
	}
}
