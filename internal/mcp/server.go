package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/julython/majordomo/internal/analyze"
)

type Server struct {
	majordomoPath string
	repoRoot      string
}

func New(repoRoot string) *Server {
	return &Server{
		majordomoPath: os.Args[0],
		repoRoot:      repoRoot,
	}
}

type Tool struct {
	Name        string
	Description string
	InputSchema ToolSchema
}

type ToolSchema struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties,omitempty"`
	Required   []string            `json:"required,omitempty"`
}

type Property struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

func (s *Server) ListTools() []Tool {
	return []Tool{
		{
			Name:        "analyze",
			Description: "Scan the repository, grade it, and return structured results. Use this to understand project health, test coverage, documentation, and AI-readiness.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]Property{
					"path": {
						Type:        "string",
						Description: "Path to the repository (default: .)",
					},
					"no_llm": {
						Type:        "boolean",
						Description: "Skip LLM narrative generation (default: false)",
					},
				},
			},
		},
		{
			Name:        "knowledge",
			Description: "Show what majordomo has learned about this repository from previous analyses.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]Property{
					"topic": {
						Type:        "string",
						Description: "Filter by topic: ci, docs, tests, deps, structure",
					},
					"open_only": {
						Type:        "boolean",
						Description: "Show only unresolved suggestions",
					},
				},
			},
		},
		{
			Name:        "status",
			Description: "Show currently running jobs and their status.",
			InputSchema: ToolSchema{
				Type:       "object",
				Properties: map[string]Property{},
			},
		},
		{
			Name:        "setup",
			Description: "Initialize majordomo for a repository. Creates .majordomo directory and seeds knowledge base from initial scan.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]Property{
					"path": {
						Type:        "string",
						Description: "Path to the repository (default: .)",
					},
				},
			},
		},
	}
}

type CallResult struct {
	Content []ContentBlock
	IsError bool
}

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func (s *Server) CallTool(ctx context.Context, name string, args map[string]any) (*CallResult, error) {
	switch name {
	case "analyze":
		return s.callAnalyze(ctx, args)
	case "knowledge":
		return s.callKnowledge(ctx, args)
	case "status":
		return s.callStatus(ctx)
	case "setup":
		return s.callSetup(ctx, args)
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

func (s *Server) callAnalyze(ctx context.Context, args map[string]any) (*CallResult, error) {
	path := "."
	if p, ok := args["path"].(string); ok && p != "" {
		path = p
	}
	noLLM := false
	if b, ok := args["no_llm"].(bool); ok {
		noLLM = b
	}

	slog.Info("MCP: analyze", "path", path, "no_llm", noLLM)

	// Run analyze with JSON output
	cmd := exec.Command(s.majordomoPath, "analyze", path, "--json")
	if noLLM {
		cmd.Args = append(cmd.Args, "--no-llm")
	}
	cmd.Env = append(os.Environ(), "NO_COLOR=1")

	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return &CallResult{
				Content: []ContentBlock{
					{Type: "text", Text: string(exitErr.Stderr)},
				},
				IsError: true,
			}, nil
		}
		return nil, fmt.Errorf("analyze failed: %w", err)
	}

	// Parse JSON output
	var result analyze.Output
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("parse output: %w", err)
	}

	// Format as readable text for the agent
	var b strings.Builder
	b.WriteString("## Analysis Results\n\n")
	b.WriteString(fmt.Sprintf("**Grade:** %s (%d%%)\n\n", result.Grade.Letter, int(result.Grade.OverallPct)))

	b.WriteString("### Categories\n\n")
	for _, cat := range result.Grade.Categories {
		b.WriteString(fmt.Sprintf("**%s:** %d%%\n", cat.Name, int(cat.Pct)))
		for _, sig := range cat.Signals {
			status := "✓"
			if !sig.Passed {
				status = "✗"
			}
			b.WriteString(fmt.Sprintf("  %s %s\n", status, sig.Name))
		}
		b.WriteString("\n")
	}

	if result.Narrative != "" {
		b.WriteString("### LLM Assessment\n\n")
		b.WriteString(result.Narrative)
	}

	return &CallResult{
		Content: []ContentBlock{
			{Type: "text", Text: b.String()},
		},
	}, nil
}

func (s *Server) callKnowledge(ctx context.Context, args map[string]any) (*CallResult, error) {
	topic := ""
	if t, ok := args["topic"].(string); ok {
		topic = t
	}
	openOnly := false
	if b, ok := args["open_only"].(bool); ok {
		openOnly = b
	}

	slog.Info("MCP: knowledge", "topic", topic, "open_only", openOnly)

	cmd := exec.Command(s.majordomoPath, "knowledge")
	if topic != "" {
		cmd.Args = append(cmd.Args, topic)
	}
	if openOnly {
		cmd.Args = append(cmd.Args, "--open")
	}
	cmd.Env = append(os.Environ(), "NO_COLOR=1")

	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return &CallResult{
				Content: []ContentBlock{
					{Type: "text", Text: string(exitErr.Stderr)},
				},
				IsError: true,
			}, nil
		}
		return nil, fmt.Errorf("knowledge failed: %w", err)
	}

	return &CallResult{
		Content: []ContentBlock{
			{Type: "text", Text: string(out)},
		},
	}, nil
}

func (s *Server) callStatus(ctx context.Context) (*CallResult, error) {
	slog.Info("MCP: status")

	cmd := exec.Command(s.majordomoPath, "status")
	cmd.Env = append(os.Environ(), "NO_COLOR=1")

	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return &CallResult{
				Content: []ContentBlock{
					{Type: "text", Text: string(exitErr.Stderr)},
				},
				IsError: true,
			}, nil
		}
		return nil, fmt.Errorf("status failed: %w", err)
	}

	return &CallResult{
		Content: []ContentBlock{
			{Type: "text", Text: string(out)},
		},
	}, nil
}

func (s *Server) callSetup(ctx context.Context, args map[string]any) (*CallResult, error) {
	path := "."
	if p, ok := args["path"].(string); ok && p != "" {
		path = p
	}

	slog.Info("MCP: setup", "path", path)

	cmd := exec.Command(s.majordomoPath, "setup", path)
	cmd.Env = append(os.Environ(), "NO_COLOR=1")

	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return &CallResult{
				Content: []ContentBlock{
					{Type: "text", Text: string(exitErr.Stderr)},
				},
				IsError: true,
			}, nil
		}
		return nil, fmt.Errorf("setup failed: %w", err)
	}

	return &CallResult{
		Content: []ContentBlock{
			{Type: "text", Text: string(out)},
		},
	}, nil
}
