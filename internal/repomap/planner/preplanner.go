package planner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/julython/majordomo/internal/repomap/graph"
)

// PrePlanRequest represents what the LLM needs to see before planning.
type PrePlanRequest struct {
	Summary string   `json:"summary"` // brief description of approach
	Symbols []string `json:"symbols"` // symbol names to examine (e.g., "HandleRequest")
	Files   []string `json:"files"`   // file paths to read (e.g., "api/handler.go")
}

// PrePlanResponse is parsed from the LLM's pre-planning output.
type PrePlanResponse struct {
	Summary string   `json:"summary"`
	Symbols []string `json:"symbols"`
	Files   []string `json:"files"`
}

// BuildPrePlanningPrompt creates the first-phase prompt that asks the LLM
// to identify what code it needs to examine before creating a plan.
func (p *Planner) BuildPrePlanningPrompt(task string) string {
	var b strings.Builder

	b.WriteString("You are an expert prompt engineer. Your job is to analyze a coding task ")
	b.WriteString("and determine what code context is needed to create an implementation plan.\n\n")

	b.WriteString("## Your task\n")
	b.WriteString("1. Analyze the user's request below\n")
	b.WriteString("2. Examine the repository structure provided\n")
	b.WriteString("3. Identify which specific symbols (functions/classes/types) need to be examined\n")
	b.WriteString("4. Identify which files contain relevant context\n")
	b.WriteString("5. Write a brief summary of your initial approach\n\n")

	b.WriteString("## Repository structure\n")
	b.WriteString(p.buildSkeleton())
	b.WriteString("\n")

	b.WriteString("## User request\n")
	b.WriteString(task)
	b.WriteString("\n\n")

	b.WriteString("## Response format\n")
	b.WriteString("Respond with a JSON object listing what you need to examine:\n")
	b.WriteString("```json\n")
	b.WriteString("{\n")
	b.WriteString("  \"summary\": \"brief description of your approach\",\n")
	b.WriteString("  \"symbols\": [\"FunctionName\", \"ClassName\", \"TypeName\"],\n")
	b.WriteString("  \"files\": [\"path/to/file.go\", \"path/to/another.py\"]\n")
	b.WriteString("}\n")
	b.WriteString("```\n\n")

	b.WriteString("Guidelines:\n")
	b.WriteString("- Only request symbols/files that exist in the repository structure above\n")
	b.WriteString("- Request symbols that will need to be modified or that provide important context\n")
	b.WriteString("- Request files that contain configuration, types, or architectural patterns\n")
	b.WriteString("- Be selective — only request what's truly needed (max 10 symbols, max 5 files)\n")
	b.WriteString("- If you need to see imports or dependencies, request the files instead of individual symbols\n")

	return b.String()
}

// ParsePrePlanResponse extracts the PrePlanResponse from LLM output.
func ParsePrePlanResponse(raw string) (*PrePlanResponse, error) {
	// Strategy 1: extract JSON from fenced code block
	if idx := strings.Index(raw, "```json"); idx != -1 {
		start := idx + 7
		end := strings.Index(raw[start:], "```")
		if end != -1 {
			return parsePrePlanJSON(raw[start : start+end])
		}
	}

	// Strategy 2: generic code fence
	if idx := strings.Index(raw, "```"); idx != -1 {
		start := idx + 3
		// Skip language identifier
		if nl := strings.IndexByte(raw[start:], '\n'); nl != -1 {
			start = start + nl + 1
		}
		end := strings.Index(raw[start:], "```")
		if end != -1 {
			if resp, err := parsePrePlanJSON(raw[start : start+end]); err == nil {
				return resp, nil
			}
		}
	}

	// Strategy 3: find raw JSON object
	if start := strings.IndexByte(raw, '{'); start != -1 {
		depth := 0
		for i := start; i < len(raw); i++ {
			switch raw[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					if resp, err := parsePrePlanJSON(raw[start : i+1]); err == nil {
						return resp, nil
					}
				}
			}
		}
	}

	return nil, fmt.Errorf("could not parse pre-plan response from output")
}

func parsePrePlanJSON(s string) (*PrePlanResponse, error) {
	s = strings.TrimSpace(s)
	s = repairJSON(s)

	var resp PrePlanResponse
	if err := json.Unmarshal([]byte(s), &resp); err != nil {
		return nil, fmt.Errorf("json parse: %w", err)
	}

	return &resp, nil
}

// BuildEnhancedPlanningPrompt creates the second-phase prompt with full context
// including the source code of requested symbols and files.
func (p *Planner) BuildEnhancedPlanningPrompt(task string, prePlan *PrePlanResponse, context map[string]string) string {
	var b strings.Builder

	b.WriteString("You are a senior engineer planning changes to a codebase. ")
	b.WriteString("You've examined the relevant code below. Now create a detailed, step-by-step implementation plan.\n\n")

	b.WriteString("## Available operations\n")
	b.WriteString("- modify: edit an existing function/class/type\n")
	b.WriteString("- create: create a new file\n")
	b.WriteString("- add: add a new function/class/type to an existing file\n")
	b.WriteString("- delete: remove a function/class/type\n")
	b.WriteString("- run: execute a shell command (tests, build)\n\n")

	b.WriteString("## Initial approach\n")
	b.WriteString(prePlan.Summary)
	b.WriteString("\n\n")

	// Include the requested code context
	if len(context) > 0 {
		b.WriteString("## Relevant code context\n")
		for path, content := range context {
			lang := p.langFence(path)
			b.WriteString(fmt.Sprintf("\n### %s\n", path))
			b.WriteString(fmt.Sprintf("```%s\n%s\n```\n", lang, content))
		}
		b.WriteString("\n")
	}

	b.WriteString("## Repository structure\n")
	b.WriteString(p.buildSkeleton())
	b.WriteString("\n")

	b.WriteString("## Task\n")
	b.WriteString(task)
	b.WriteString("\n\n")

	b.WriteString("## Instructions\n")
	b.WriteString("Create a detailed implementation plan with specific edit instructions.\n")
	b.WriteString("For each step, be explicit about:\n")
	b.WriteString("- Which exact file and symbol to modify\n")
	b.WriteString("- What specific changes to make (add parameter, change logic, etc.)\n")
	b.WriteString("- Why this change is necessary\n\n")

	b.WriteString("## Response format\n")
	b.WriteString("Respond with a JSON object:\n")
	b.WriteString("```json\n")
	b.WriteString("{\n")
	b.WriteString("  \"summary\": \"one-line description of the approach\",\n")
	b.WriteString("  \"steps\": [\n")
	b.WriteString("    {\"action\": \"modify\", \"target\": \"SymbolName\", \"file\": \"path/to/file.go\", \"task\": \"detailed instruction on what to change and why\"},\n")
	b.WriteString("    {\"action\": \"create\", \"file\": \"path/to/new.go\", \"task\": \"what this file should contain and its purpose\"},\n")
	b.WriteString("    {\"action\": \"add\", \"target\": \"NewFuncName\", \"file\": \"path/to/file.go\", \"after\": \"ExistingFunc\", \"task\": \"what the new function does\"},\n")
	b.WriteString("    {\"action\": \"run\", \"command\": \"go test ./...\", \"task\": \"verify changes\"}\n")
	b.WriteString("  ]\n")
	b.WriteString("}\n")
	b.WriteString("```\n")

	return b.String()
}

// GatherContext fetches the actual source code for requested symbols and files.
func (p *Planner) GatherContext(prePlan *PrePlanResponse) (map[string]string, error) {
	context := make(map[string]string)

	// Gather requested files
	for _, filePath := range prePlan.Files {
		if fileNode, ok := p.Graph.Files[filePath]; ok {
			// Read the actual file content
			content, err := readFileContent(p.Root, filePath)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", filePath, err)
			}
			context[filePath] = content
			_ = fileNode // use fileNode if needed for metadata
		}
	}

	// Gather requested symbols (with their surrounding context)
	for _, symbolName := range prePlan.Symbols {
		symbol := p.findSymbol(symbolName, "")
		if symbol == nil {
			continue // skip if not found
		}

		// Read the symbol's body
		content, err := readSymbolContent(p.Root, symbol)
		if err != nil {
			return nil, fmt.Errorf("read symbol %s: %w", symbolName, err)
		}

		// Store with a key that includes the file path
		key := fmt.Sprintf("%s:%s", symbol.File, symbol.Name)
		context[key] = content
	}

	return context, nil
}

// Helper functions to read actual file content
func readFileContent(root, path string) (string, error) {
	fullPath := filepath.Join(root, path)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(content), nil
}

func readSymbolContent(root string, sym *graph.Symbol) (string, error) {
	// Read the file and extract the symbol's body
	content, err := readFileContent(root, sym.File)
	if err != nil {
		return "", err
	}

	// Use the BodyFrom method to extract the symbol's source
	body := sym.BodyFrom([]byte(content))
	if body == "" {
		return "", fmt.Errorf("could not extract body for symbol %s", sym.Name)
	}

	return body, nil
}
