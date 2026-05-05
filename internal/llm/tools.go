package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Message represents a chat message in the conversation.
type Message struct {
	Role       string      `json:"role"`                 // "system", "user", "assistant", "tool"
	Content    string      `json:"content,omitempty"`    // Text content
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"` // For assistant messages with tool calls
	ToolCallID string      `json:"tool_call_id,omitempty"` // For tool response messages
	Name       string      `json:"name,omitempty"`       // Tool name for tool messages
}

// ToolCall represents a function call request from the LLM.
type ToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"` // "function"
	Function Function `json:"function"`
}

// Function represents the function to call.
type Function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// Tool defines a tool/function the LLM can call.
type Tool struct {
	Type     string       `json:"type"` // "function"
	Function ToolFunction `json:"function"`
}

// ToolFunction describes a callable function.
type ToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// StreamEvent represents events during streaming with tools.
type StreamEvent struct {
	Type string // "token", "tool_call", "done", "error"

	// For token events
	Token string

	// For tool_call events
	ToolCall *ToolCall

	// For error events
	Error error
}

// ChatWithTools sends a message with tool support and streams responses.
// The callback receives tokens and tool calls as they arrive.
// Returns the final assistant message and any error.
func (c *LocalClient) ChatWithTools(ctx context.Context, messages []Message, tools []Tool, onEvent func(StreamEvent)) (*Message, error) {
	body := map[string]any{
		"model":    c.model,
		"messages": messages,
		"stream":   true,
	}

	if len(tools) > 0 {
		body["tools"] = tools
		body["tool_choice"] = "auto"
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx,
		"POST", c.baseURL+"/v1/chat/completions", bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.provider, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: status %d", c.provider, resp.StatusCode)
	}

	// Parse the streaming response
	return c.parseToolStreamResponse(ctx, resp, onEvent)
}

// parseToolStreamResponse handles SSE streaming with tool calls.
func (c *LocalClient) parseToolStreamResponse(ctx context.Context, resp *http.Response, onEvent func(StreamEvent)) (*Message, error) {
	scanner := bufio.NewScanner(resp.Body)
	var contentBuf strings.Builder
	var toolCalls []ToolCall
	toolCallsMap := make(map[int]*ToolCall) // Track partial tool calls by index

	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string     `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}

		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		for _, choice := range chunk.Choices {
			// Handle content tokens
			if choice.Delta.Content != "" {
				contentBuf.WriteString(choice.Delta.Content)
				if onEvent != nil {
					onEvent(StreamEvent{
						Type:  "token",
						Token: choice.Delta.Content,
					})
				}
			}

			// Handle tool calls (accumulated across chunks)
			for _, tc := range choice.Delta.ToolCalls {
				if _, exists := toolCallsMap[tc.Index]; !exists {
					toolCallsMap[tc.Index] = &ToolCall{
						ID:   tc.ID,
						Type: tc.Type,
						Function: Function{
							Name:      tc.Function.Name,
							Arguments: tc.Function.Arguments,
						},
					}
				} else {
					// Append to existing tool call
					toolCallsMap[tc.Index].Function.Arguments += tc.Function.Arguments
				}
			}
		}
	}

	// Convert map to slice
	for i := 0; i < len(toolCallsMap); i++ {
		if tc, ok := toolCallsMap[i]; ok {
			toolCalls = append(toolCalls, *tc)
			if onEvent != nil {
				onEvent(StreamEvent{
					Type:     "tool_call",
					ToolCall: tc,
				})
			}
		}
	}

	msg := &Message{
		Role:    "assistant",
		Content: contentBuf.String(),
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}

	if onEvent != nil {
		onEvent(StreamEvent{Type: "done"})
	}

	return msg, scanner.Err()
}
