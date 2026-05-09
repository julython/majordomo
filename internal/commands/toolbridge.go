package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/julython/majordomo/internal/llm"
)

// ToolBridge converts commands to LLM tools and handles execution.
type ToolBridge struct {
	registry *Registry
}

// NewToolBridge creates a bridge between commands and LLM tools.
func NewToolBridge(reg *Registry) *ToolBridge {
	return &ToolBridge{registry: reg}
}

// GetTools returns all commands as LLM tool definitions.
// Excludes hidden commands and meta commands like help/quit/clear.
func (tb *ToolBridge) GetTools() []llm.Tool {
	var tools []llm.Tool
	excluded := map[string]bool{
		"help": true, "quit": true, "exit": true, "clear": true, "cls": true,
		"chat": true, // Don't let the LLM call chat recursively
	}

	for name, cmd := range tb.registry.commands {
		if cmd.Hidden || excluded[name] {
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
