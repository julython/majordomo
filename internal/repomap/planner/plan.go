package planner

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Action is what a single plan step does.
type Action string

const (
	ActionModify Action = "modify" // edit an existing symbol
	ActionCreate Action = "create" // create a new file
	ActionAdd    Action = "add"    // add a new symbol to an existing file
	ActionDelete Action = "delete" // remove a symbol
	ActionRun    Action = "run"    // run a shell command (tests, build, lint)
)

// Step is a single operation in a plan.
type Step struct {
	Action  Action `json:"action"`
	Target  string `json:"target,omitempty"`  // symbol name (for modify/add/delete)
	File    string `json:"file,omitempty"`    // file path (for create/add/modify)
	Task    string `json:"task"`              // what to do, in natural language
	Command string `json:"command,omitempty"` // shell command (for run)
	After   string `json:"after,omitempty"`   // insert after this symbol (for add)
}

// Plan is a sequence of steps to accomplish a task.
type Plan struct {
	Summary string `json:"summary"` // one-line description of the overall approach
	Steps   []Step `json:"steps"`
}

// String renders the plan as human-readable text.
func (p *Plan) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Plan: %s\n", p.Summary)
	fmt.Fprintf(&b, "Steps: %d\n\n", len(p.Steps))
	for i, s := range p.Steps {
		fmt.Fprintf(&b, "  %d. [%s]", i+1, s.Action)
		if s.Target != "" {
			fmt.Fprintf(&b, " %s", s.Target)
		}
		if s.File != "" {
			fmt.Fprintf(&b, " in %s", s.File)
		}
		if s.Command != "" {
			fmt.Fprintf(&b, " `%s`", s.Command)
		}
		fmt.Fprintf(&b, "\n     %s\n", s.Task)
	}
	return b.String()
}

// ParsePlan extracts a Plan from LLM output. It tries multiple strategies:
// 1. JSON block (```json ... ```)
// 2. Raw JSON object
// 3. XML-style <plan> blocks (fallback for models that struggle with JSON)
func ParsePlan(raw string) (*Plan, error) {
	// Strategy 1: extract JSON from fenced code block
	if idx := strings.Index(raw, "```json"); idx != -1 {
		start := idx + 7
		end := strings.Index(raw[start:], "```")
		if end != -1 {
			return parseJSON(raw[start : start+end])
		}
	}

	// Also try generic code fence
	if idx := strings.Index(raw, "```"); idx != -1 {
		start := idx + 3
		// Skip language identifier on same line
		if nl := strings.IndexByte(raw[start:], '\n'); nl != -1 {
			start = start + nl + 1
		}
		end := strings.Index(raw[start:], "```")
		if end != -1 {
			if p, err := parseJSON(raw[start : start+end]); err == nil {
				return p, nil
			}
		}
	}

	// Strategy 2: raw JSON — find the outermost { }
	if start := strings.IndexByte(raw, '{'); start != -1 {
		depth := 0
		for i := start; i < len(raw); i++ {
			switch raw[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					if p, err := parseJSON(raw[start : i+1]); err == nil {
						return p, nil
					}
				}
			}
		}
	}

	// Strategy 3: line-based fallback for models that can't do JSON
	return parseLineBased(raw)
}

func parseJSON(s string) (*Plan, error) {
	s = strings.TrimSpace(s)

	// Attempt repair: strip trailing commas before } or ]
	s = repairJSON(s)

	var plan Plan
	if err := json.Unmarshal([]byte(s), &plan); err != nil {
		return nil, fmt.Errorf("json parse: %w", err)
	}

	if len(plan.Steps) == 0 {
		return nil, fmt.Errorf("plan has no steps")
	}

	return &plan, nil
}

// repairJSON fixes common LLM JSON mistakes.
func repairJSON(s string) string {
	// Remove trailing commas before } or ]
	s = strings.ReplaceAll(s, ",}", "}")
	s = strings.ReplaceAll(s, ",]", "]")
	// Remove trailing comma before newline + } or ]
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if i+1 < len(lines) {
			nextTrimmed := strings.TrimSpace(lines[i+1])
			if strings.HasSuffix(trimmed, ",") &&
				(strings.HasPrefix(nextTrimmed, "}") || strings.HasPrefix(nextTrimmed, "]")) {
				lines[i] = line[:len(line)-len(trimmed)] + strings.TrimSuffix(trimmed, ",")
			}
		}
	}
	return strings.Join(lines, "\n")
}

// parseLineBased handles output like:
//
//	STEP 1: modify HandleRequest in api/handler.go
//	TASK: Add rate limiting
//
//	STEP 2: create api/ratelimit.go
//	TASK: Create RateLimiter type
func parseLineBased(raw string) (*Plan, error) {
	plan := &Plan{}
	lines := strings.Split(raw, "\n")

	var current *Step
	for _, line := range lines {
		line = strings.TrimSpace(line)
		upper := strings.ToUpper(line)

		// Look for STEP lines
		if strings.HasPrefix(upper, "STEP") {
			if current != nil {
				plan.Steps = append(plan.Steps, *current)
			}
			current = &Step{}

			// Parse: STEP N: <action> <target> [in <file>]
			after := line
			if idx := strings.IndexByte(after, ':'); idx != -1 {
				after = strings.TrimSpace(after[idx+1:])
			}
			current.Action, current.Target, current.File = parseStepLine(after)
			continue
		}

		// Look for TASK lines
		if strings.HasPrefix(upper, "TASK:") {
			if current != nil {
				current.Task = strings.TrimSpace(line[5:])
			}
			continue
		}

		// Look for SUMMARY line
		if strings.HasPrefix(upper, "SUMMARY:") {
			plan.Summary = strings.TrimSpace(line[8:])
			continue
		}

		// Continuation of task description
		if current != nil && current.Task != "" && line != "" {
			current.Task += " " + line
		}
	}
	if current != nil {
		plan.Steps = append(plan.Steps, *current)
	}

	if len(plan.Steps) == 0 {
		return nil, fmt.Errorf("could not parse any steps from output")
	}

	return plan, nil
}

// parseStepLine extracts action, target, file from "modify HandleRequest in api/handler.go"
func parseStepLine(s string) (Action, string, string) {
	s = strings.TrimSpace(s)
	parts := strings.Fields(s)
	if len(parts) == 0 {
		return ActionModify, "", ""
	}

	action := Action(strings.ToLower(parts[0]))
	switch action {
	case ActionModify, ActionCreate, ActionAdd, ActionDelete, ActionRun:
		// valid
	default:
		action = ActionModify
	}

	if action == ActionRun {
		cmd := strings.Join(parts[1:], " ")
		return action, "", cmd
	}

	if action == ActionCreate {
		file := ""
		if len(parts) > 1 {
			file = parts[1]
		}
		return action, "", file
	}

	// modify/add/delete: look for "in <file>"
	target := ""
	file := ""
	if len(parts) > 1 {
		target = parts[1]
	}
	for i, p := range parts {
		if strings.ToLower(p) == "in" && i+1 < len(parts) {
			file = parts[i+1]
			break
		}
	}

	return action, target, file
}
