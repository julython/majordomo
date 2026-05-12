package planner

import (
	"strings"
	"testing"

	"github.com/julython/majordomo/internal/repomap/graph"
)

func TestBuildPrePlanningPrompt(t *testing.T) {
	// Create a minimal graph for testing
	g := graph.New("/tmp/test")
	g.Files["test.go"] = &graph.FileNode{
		Path:     "test.go",
		Language: graph.LangGo,
		Package:  "main",
	}
	g.Symbols[graph.SymbolID("test.go::main")] = &graph.Symbol{
		ID:        graph.SymbolID("test.go::main"),
		Name:      "main",
		Kind:      graph.KindFunction,
		File:      "test.go",
		Language:  graph.LangGo,
		Signature: "func main()",
		Exported:  false,
	}

	p := NewPlanner(g, "/tmp/test")
	prompt := p.BuildPrePlanningPrompt("add a new feature")

	// Verify key sections are present
	if !strings.Contains(prompt, "prompt engineer") {
		t.Error("Missing prompt engineer instruction")
	}
	if !strings.Contains(prompt, "Repository structure") {
		t.Error("Missing repository structure section")
	}
	if !strings.Contains(prompt, "add a new feature") {
		t.Error("Missing user request")
	}
	if !strings.Contains(prompt, "Response format") {
		t.Error("Missing response format")
	}
	if !strings.Contains(prompt, `"symbols"`) {
		t.Error("Missing symbols field in example")
	}
}

func TestParsePrePlanResponse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name: "valid json block",
			input: `Here's what I need:
` + "```json\n" + `{
  "summary": "test approach",
  "symbols": ["Foo", "Bar"],
  "files": ["test.go"]
}
` + "```",
			wantErr: false,
		},
		{
			name: "valid raw json",
			input: `{
  "summary": "test approach",
  "symbols": ["Foo"],
  "files": []
}`,
			wantErr: false,
		},
		{
			name: "json with trailing comma",
			input: `{
  "summary": "test",
  "symbols": ["Foo",],
  "files": []
}`,
			wantErr: false,
		},
		{
			name:    "no json",
			input:   "This is just text with no JSON",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := ParsePrePlanResponse(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParsePrePlanResponse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if resp == nil {
					t.Error("Expected response, got nil")
				}
				if resp.Summary == "" {
					t.Error("Expected non-empty summary")
				}
			}
		})
	}
}

func TestBuildEnhancedPlanningPrompt(t *testing.T) {
	g := graph.New("/tmp/test")
	p := NewPlanner(g, "/tmp/test")

	prePlan := &PrePlanResponse{
		Summary: "Add middleware",
		Symbols: []string{"HandleRequest"},
		Files:   []string{"server.go"},
	}

	context := map[string]string{
		"server.go":                    "package main\n\nfunc main() {}\n",
		"handler.go:HandleRequest": "func HandleRequest() error { return nil }",
	}

	prompt := p.BuildEnhancedPlanningPrompt("add rate limiting", prePlan, context)

	// Verify key sections
	if !strings.Contains(prompt, "Add middleware") {
		t.Error("Missing initial approach")
	}
	if !strings.Contains(prompt, "Relevant code context") {
		t.Error("Missing code context section")
	}
	if !strings.Contains(prompt, "server.go") {
		t.Error("Missing context file")
	}
	if !strings.Contains(prompt, "package main") {
		t.Error("Missing file content")
	}
	if !strings.Contains(prompt, "add rate limiting") {
		t.Error("Missing task")
	}
}

func TestGatherContext(t *testing.T) {
	// This test would require actual files, so we'll skip it for now
	// In a real scenario, you'd create temp files and test file reading
	t.Skip("Requires filesystem setup")
}
