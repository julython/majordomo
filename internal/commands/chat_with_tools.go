package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/julython/majordomo/internal/knowledge"
	"github.com/julython/majordomo/internal/llm"
)

// chatWithToolsCommand enables the LLM to call commands as tools.
func chatWithToolsCommand(deps *Deps, reg *Registry) *Command {
	return &Command{
		Name:        "chat",
		Aliases:     []string{"ask"},
		Description: "Ask the LLM about this repo (with tool support)",
		Usage:       "/chat <message>",
		Category:    "general",
		Args: []Arg{
			{Name: "message", Description: "Your question or request"},
		},
		Run: func(ctx context.Context, args ParsedArgs, sink Sink) error {
			if deps.LLM == nil {
				sink.Error("No LLM available. Start ollama or another local model, then restart majordomo.")
				return nil
			}

			// Check if the LLM client supports tool calling
			toolClient, supportsTools := deps.LLM.(*llm.LocalClient)
			if !supportsTools {
				sink.Error("Tool calling not supported by this LLM client")
				return nil
			}

			message := strings.Join(args.Positional, " ")
			if message == "" {
				message = args.Raw
			}
			if message == "" {
				sink.Error("Say something! e.g. /chat how do I run the tests?")
				return nil
			}

			kb, err := knowledge.Open(deps.RepoDir)
			if err != nil {
				kb = &knowledge.Store{}
			}

			// Build the system message and initial user message
			systemPrompt := buildSystemPrompt(kb)
			messages := []llm.Message{
				{Role: "system", Content: systemPrompt},
				{Role: "user", Content: message},
			}

			// Get available tools
			bridge := NewToolBridge(reg)
			tools := bridge.GetTools()

			sink.Status(fmt.Sprintf("Thinking (%s)...", deps.LLM.Name()))

			// Tool calling loop - continue until we get a text response
			for {
				if ctx.Err() != nil {
					return nil
				}

				// Stream the response
				var lineBuf strings.Builder
				msg, err := toolClient.ChatWithTools(ctx, messages, tools, func(event llm.StreamEvent) {
					switch event.Type {
					case "token":
						// Stream markdown tokens to the UI
						for _, ch := range event.Token {
							if ch == '\n' {
								sink.PrintMarkdown(lineBuf.String())
								lineBuf.Reset()
							} else {
								lineBuf.WriteRune(ch)
							}
						}
					case "tool_call":
						// Show that a tool is being called
						sink.Print(fmt.Sprintf("🔧 Calling tool: %s", event.ToolCall.Function.Name))
					}
				})

				// Flush any remaining content
				if lineBuf.Len() > 0 {
					sink.PrintMarkdown(lineBuf.String())
				}

				if err != nil {
					sink.Error(fmt.Sprintf("LLM error: %v", err))
					return nil
				}

				// Add assistant's response to conversation
				messages = append(messages, *msg)

				// If no tool calls, we're done
				if len(msg.ToolCalls) == 0 {
					break
				}

				// Execute each tool call and add results
				for _, toolCall := range msg.ToolCalls {
					result, err := bridge.ExecuteTool(ctx, toolCall, sink)
					if err != nil {
						result = fmt.Sprintf("Error executing tool: %v", err)
						sink.Error(result)
					}

					// Add tool result to conversation
					messages = append(messages, llm.Message{
						Role:       "tool",
						Content:    result,
						ToolCallID: toolCall.ID,
						Name:       toolCall.Function.Name,
					})
				}

				// Continue the loop to let the LLM respond to tool results
			}


			return nil
		},
	}
}

// buildSystemPrompt creates the system message for the LLM.
func buildSystemPrompt(kb *knowledge.Store) string {
	var b strings.Builder

	b.WriteString(`You are majordomo, an AI assistant that helps developers understand and improve their projects.

You have access to tools that let you analyze repositories, check knowledge, and perform actions. Use these tools when needed to answer questions accurately.

Be direct and helpful. Give concrete commands and file paths when relevant. Keep answers focused — you're a terminal tool, not a blog post.

`)

	if kbCtx := kb.ForLLM(); kbCtx != "" {
		b.WriteString("### What you know about this repo:\n")
		b.WriteString(kbCtx)
		b.WriteString("\n")
	}

	if kb.LastReport != nil {
		b.WriteString("### Last scan data is available (repo has been analyzed before).\n\n")
	} else {
		b.WriteString("### This repo has not been analyzed yet. You can use the 'analyze' tool if needed.\n\n")
	}

	return b.String()
}
