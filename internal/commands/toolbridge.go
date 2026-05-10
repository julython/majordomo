package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/julython/majordomo/internal/llm"
	"github.com/julython/majordomo/internal/repomap/executor"
)

// ToolBridge converts commands to LLM tools and handles execution.
type ToolBridge struct {
	registry *Registry
	executor *executor.Executor
	excluded map[string]bool
}

// NewToolBridge creates a bridge between commands and LLM tools.
func NewToolBridge(reg *Registry, e *executor.Executor) *ToolBridge {
	return &ToolBridge{
		registry: reg,
		executor: e,
		excluded: map[string]bool{
			"help": true, "quit": true, "exit": true, "clear": true, "cls": true,
			"chat": true,
		},
	}
}

// GetTools returns all commands as LLM tool definitions.
// Excludes hidden commands and meta commands.
func (tb *ToolBridge) GetTools() []llm.Tool {
	var tools []llm.Tool

	for name, cmd := range tb.registry.commands {
		if cmd.Hidden || tb.excluded[name] {
			continue
		}

		tool := llm.Tool{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        cmd.Name,
				Description: cmd.Description,
				Parameters:  tb.buildParameterSchema(cmd),
			},
		}
		tools = append(tools, tool)
	}

	// Add symbol_content read-only tool
	tools = append(tools, llm.Tool{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "symbol_content",
			Description: "Get the source code of a symbol by name in a file. Re-indexes repo before lookup.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file": map[string]interface{}{
						"type":        "string",
						"description": "File path (repo-relative)",
					},
					"symbol": map[string]interface{}{
						"type":        "string",
						"description": "Symbol name to look up",
					},
				},
				"required": []string{"file", "symbol"},
			},
		},
	})

	return tools
}

// buildParameterSchema converts command Args to JSON Schema.
func (tb *ToolBridge) buildParameterSchema(cmd *Command) map[string]interface{} {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": make(map[string]interface{}),
	}

	var required []string
	properties := schema["properties"].(map[string]interface{})

	for _, arg := range cmd.Args {
		prop := map[string]interface{}{
			"description": arg.Description,
		}

		if arg.IsFlag {
			prop["type"] = "boolean"
		} else {
			prop["type"] = "string"
		}

		if arg.Default != "" {
			prop["default"] = arg.Default
		}

		properties[arg.Name] = prop

		if arg.Required {
			required = append(required, arg.Name)
		}
	}

	if len(required) > 0 {
		schema["required"] = required
	}

	return schema
}

// ExecuteTool executes a tool call and returns the result as a string.
func (tb *ToolBridge) ExecuteTool(ctx context.Context, toolCall llm.ToolCall, sink Sink) (string, error) {
	// Handle symbol_content (read-only, built-in tool)
	if toolCall.Function.Name == "symbol_content" {
		var args map[string]interface{}
		if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
		file, _ := args["file"].(string)
		symbol, _ := args["symbol"].(string)
		if file == "" || symbol == "" {
			return "", fmt.Errorf("symbol_content requires 'file' and 'symbol'")
		}
		return tb.executor.SymbolContent(file, symbol)
	}

	cmd, ok := tb.registry.Get(toolCall.Function.Name)
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", toolCall.Function.Name)
	}

	// Parse the arguments JSON
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	// Convert to ParsedArgs format
	parsedArgs := tb.convertToCommandArgs(args, cmd)

	// Execute the command and capture output
	captureSink := NewCaptureSink()
	if err := cmd.Run(ctx, parsedArgs, captureSink); err != nil {
		return "", err
	}

	return captureSink.GetOutput(), nil
}

// convertToCommandArgs converts tool arguments to ParsedArgs.
func (tb *ToolBridge) convertToCommandArgs(args map[string]interface{}, cmd *Command) ParsedArgs {
	pa := ParsedArgs{
		Flags: make(map[string]string),
	}

	for key, value := range args {
		// Check if this is a positional or flag argument
		isFlag := false
		for _, arg := range cmd.Args {
			if arg.Name == key {
				isFlag = arg.IsFlag
				break
			}
		}

		if isFlag {
			if b, ok := value.(bool); ok && b {
				pa.Flags[key] = "true"
			} else {
				pa.Flags[key] = "false"
			}
		} else {
			// Could be positional or named flag
			if str, ok := value.(string); ok {
				// Try positional first
				pa.Positional = append(pa.Positional, str)
				pa.Flags[key] = str
			}
		}
	}

	return pa
}

// ExecutePlan executes each step in the plan using the executor.
// Called by the chat command after the LLM loop completes.
func (tb *ToolBridge) ExecutePlan(ctx context.Context, plan *Plan) error {
	for i, step := range plan.Steps {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		switch step.Action {
		case "modify":
			oldBody, err := tb.executor.ReplaceSymbol(step.File, step.Target, step.Task)
			if err != nil {
				return fmt.Errorf("step %d (modify %s in %s): %w", i+1, step.Target, step.File, err)
			}
			fmt.Printf("Modified %s in %s (old: %s chars)\n", step.Target, step.File, len(oldBody))

		case "add":
			err := tb.executor.InsertAfter(step.File, step.Target, step.Task)
			if err != nil {
				return fmt.Errorf("step %d (add after %s in %s): %w", i+1, step.Target, step.File, err)
			}
			fmt.Printf("Added code after %s in %s\n", step.Target, step.File)

		case "delete":
			err := tb.executor.DeleteSymbol(step.File, step.Target)
			if err != nil {
				return fmt.Errorf("step %d (delete %s in %s): %w", i+1, step.Target, step.File, err)
			}
			fmt.Printf("Deleted %s from %s\n", step.Target, step.File)

		case "create":
			err := tb.executor.WriteFile(step.File, []byte(step.Task))
			if err != nil {
				return fmt.Errorf("step %d (create %s): %w", i+1, step.File, err)
			}
			fmt.Printf("Created %s\n", step.File)

		case "run":
			parts := strings.Fields(step.Task)
			if len(parts) == 0 {
				return fmt.Errorf("step %d (run): empty command", i+1)
			}
			output, err := tb.executor.RunCommand(tb.executor.Root, parts[0], parts[1:]...)
			if err != nil {
				fmt.Printf("Step %d (run) failed: %v\n  Output: %s\n", i+1, err, string(output))
			}
			fmt.Printf("Step %d (run): %s", i+1, strings.TrimSpace(string(output)))
		}
	}

	return nil
}

// CaptureSink captures command output for returning to the LLM.
type CaptureSink struct {
	Lines []string
}

// NewCaptureSink creates a sink that captures output.
func NewCaptureSink() *CaptureSink {
	return &CaptureSink{Lines: make([]string, 0)}
}

func (cs *CaptureSink) Print(text string)         { cs.Lines = append(cs.Lines, text) }
func (cs *CaptureSink) PrintMarkdown(text string) { cs.Lines = append(cs.Lines, text) }
func (cs *CaptureSink) PrintStyled(text string)   { cs.Lines = append(cs.Lines, text) }
func (cs *CaptureSink) Status(text string)        {}
func (cs *CaptureSink) Error(text string)         { cs.Lines = append(cs.Lines, "ERROR: "+text) }
func (cs *CaptureSink) Finish(summary string) {
	if summary != "" {
		cs.Lines = append(cs.Lines, summary)
	}
}

// GetOutput returns all captured lines as a single string.
func (cs *CaptureSink) GetOutput() string {
	return strings.Join(cs.Lines, "\n")
}

// Plan is a sequence of steps to accomplish a task.
type Plan struct {
	Summary string `json:"summary"`
	Steps   []Step `json:"steps"`
}

// Step is a single operation in a plan.
type Step struct {
	Action string `json:"action"`
	Target string `json:"target,omitempty"`
	File   string `json:"file,omitempty"`
	Task   string `json:"task"`
}
