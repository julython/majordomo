package commands

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/julython/majordomo/internal/llm"
	"github.com/julython/majordomo/internal/repomap/executor"
)

// mockToolClient is a minimal LLM mock that implements llm.ToolClient.
type mockToolClient struct {
	Response *llm.Message

	displayName string

	ChatCalls []chatCallRecord
}

type chatCallRecord struct {
	Messages []llm.Message
	Tools    []llm.Tool
}

func (m *mockToolClient) Generate(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (m *mockToolClient) Stream(_ context.Context, _ string, _ func(string)) (string, error) {
	return "", nil
}

func (m *mockToolClient) Name() string {
	if m.displayName != "" {
		return m.displayName
	}
	return "mock/0.0.1"
}

func (m *mockToolClient) ChatWithTools(_ context.Context, messages []llm.Message, _ []llm.Tool, _ func(llm.StreamEvent)) (*llm.Message, error) {
	if m != nil {
		m.ChatCalls = append(m.ChatCalls, chatCallRecord{
			Messages: messages,
		})
	}
	return m.Response, nil
}

func TestChatCommandExecutesPlan(t *testing.T) {
	planResponse := &llm.Message{
		Role:    "assistant",
		Content: "Here's the plan:\n\n```json\n{\"summary\":\"Add a greeting function\",\"steps\":[{\"action\":\"modify\",\"target\":\"Foo\",\"file\":\"main.go\",\"task\":\"func Foo() int { return 42 }\"}]}\n```",
	}

	mock := &mockToolClient{
		Response: planResponse,
	}

	dir := t.TempDir()
	goFile := `package main

func Foo() int {
	return 1
}
`
	if err := writeFile(dir, "main.go", goFile); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry()
	reg.Register(&Command{
		Name:        "help",
		Description: "Show help",
		Category:    "general",
		Run:         func(ctx context.Context, args ParsedArgs, sink Sink) error { return nil },
	})

	deps := &Deps{
		RepoDir: dir,
		LLM:     mock,
	}

	sink := &CaptureSink{}

	cmd := chatCommand(deps, reg)
	if err := cmd.Run(context.Background(), ParsedArgs{Raw: "add a greeting"}, sink); err != nil {
		t.Fatalf("chatCommand returned error: %v", err)
	}

	if len(mock.ChatCalls) == 0 {
		t.Fatal("expected ChatWithTools to be called")
	}

	output := strings.Join(sink.Lines, "\n")
	if !strings.Contains(output, "Plan:") {
		t.Errorf("expected sink output to contain 'Plan:', got:\n%s", output)
	}
	if !strings.Contains(output, "Plan completed.") {
		t.Errorf("expected sink output to contain 'Plan completed.', got:\n%s", output)
	}
}

func TestChatCommandNoLLM(t *testing.T) {
	reg := NewRegistry()
	deps := &Deps{
		RepoDir: t.TempDir(),
		LLM:     nil,
	}
	sink := &CaptureSink{}

	cmd := chatCommand(deps, reg)
	if err := cmd.Run(context.Background(), ParsedArgs{Raw: "hello"}, sink); err != nil {
		t.Fatalf("chatCommand returned error: %v", err)
	}

	output := strings.Join(sink.Lines, "\n")
	if !strings.Contains(output, "No LLM available") {
		t.Errorf("expected 'No LLM available' error, got:\n%s", output)
	}
}

func TestChatCommandNoToolSupport(t *testing.T) {
	noToolClient := &noToolClient{name: "bare/1.0"}

	reg := NewRegistry()
	deps := &Deps{
		RepoDir: t.TempDir(),
		LLM:     noToolClient,
	}
	sink := &CaptureSink{}

	cmd := chatCommand(deps, reg)
	if err := cmd.Run(context.Background(), ParsedArgs{Raw: "hello"}, sink); err != nil {
		t.Fatalf("chatCommand returned error: %v", err)
	}

	output := strings.Join(sink.Lines, "\n")
	if !strings.Contains(output, "Tool calling not supported") {
		t.Errorf("expected 'Tool calling not supported' error, got:\n%s", output)
	}
}

// noToolClient implements llm.Client but not llm.ToolClient.
type noToolClient struct{ name string }

func (n *noToolClient) Generate(_ context.Context, _ string) (string, error) { return "", nil }
func (n *noToolClient) Stream(_ context.Context, _ string, _ func(string)) (string, error) {
	return "", nil
}
func (n *noToolClient) Name() string { return n.name }

func writeFile(dir, relPath, content string) error {
	path := dir + "/" + relPath
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0644)
}

// Verify mockToolClient implements llm.ToolClient.
var _ llm.ToolClient = (*mockToolClient)(nil)

// Verify the executor is wired correctly.
var _ = executor.New
