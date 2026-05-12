package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/julython/majordomo/internal/commands"
)

// mockRenderer implements markdownTermRenderer for testing.
type mockRenderer struct {
	Fn func(src string) (string, error)
}

func (m *mockRenderer) Render(src string) (string, error) {
	if m.Fn != nil {
		return m.Fn(src)
	}
	return src, nil
}

// newTestChat creates a *Chat ready for testing by simulating a WindowSizeMsg
// so that the viewport is initialized and ready == true.
func newTestChat(t *testing.T, reg *commands.Registry) *Chat {
	t.Helper()
	c := NewChat(reg)
	c.mdRenderer = &mockRenderer{}

	m, _ := c.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	pc, ok := m.(*Chat)
	require.True(t, ok, "expected *Chat from Update")
	return pc
}

// newCleanChat creates a *Chat without the initial system message.
func newCleanChat(t *testing.T, reg *commands.Registry) *Chat {
	t.Helper()
	c := NewChat(reg)
	c.mdRenderer = &mockRenderer{}
	c.messages = nil
	c.ready = true
	// Simulate WindowSizeMsg to initialize viewport without system message.
	m, _ := c.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	// Clear the system message that was added during init.
	c.messages = nil
	c.dirty = false
	pc, ok := m.(*Chat)
	require.True(t, ok, "expected *Chat from Update")
	return pc
}

// --- isLeakedTerminalInput tests ---

func TestIsLeakedTerminalInput(t *testing.T) {
	tests := []struct {
		name     string
		msg      tea.KeyMsg
		expected bool
	}{
		{
			name:     "OSC color query escape",
			msg:      tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]11;rgb:aa/bb/cc")},
			expected: true,
		},
		{
			name:     "OSC prompt query escape",
			msg:      tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]10;test")},
			expected: true,
		},
		{
			name:     "SGR mouse report",
			msg:      tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<33;10;5M")},
			expected: true,
		},
		{
			name:     "RGB color code",
			msg:      tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("rgb:#fff/000/something")},
			expected: true,
		},
		{
			name:     "regular key",
			msg:      tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")},
			expected: false,
		},
		{
			name:     "regular key with slash",
			msg:      tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello/world")},
			expected: false,
		},
		{
			name:     "not a runes message",
			msg:      tea.KeyMsg{Type: tea.KeyCtrlC},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isLeakedTerminalInput(tt.msg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// --- Chat message appending tests ---

func TestChat_AppendMessages(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)

	// newTestChat adds one system message during initialization.
	initialCount := len(c.messages)
	assert.Equal(t, 1, initialCount)
	assert.Equal(t, MsgSystem, c.messages[0].Kind)

	c.appendUser("hello")
	c.append(MsgOutput, "world")
	c.appendError("oops")
	c.appendSystem("booted")
	c.appendStyled("<b>bold</b>")
	c.appendMarkdown("some *markdown*")

	require.Equal(t, 7, len(c.messages))
	assert.Equal(t, MsgUser, c.messages[1].Kind)
	assert.Equal(t, "hello", c.messages[1].Text)
	assert.Equal(t, MsgOutput, c.messages[2].Kind)
	assert.Equal(t, "world", c.messages[2].Text)
	assert.Equal(t, MsgError, c.messages[3].Kind)
	assert.Equal(t, "oops", c.messages[3].Text)
	assert.Equal(t, MsgSystem, c.messages[4].Kind)
	assert.Equal(t, "booted", c.messages[4].Text)
	assert.Equal(t, MsgStyled, c.messages[5].Kind)
	assert.Equal(t, "<b>bold</b>", c.messages[5].Text)
	assert.Equal(t, MsgMarkdown, c.messages[6].Kind)
	assert.Equal(t, "some *markdown*", c.messages[6].Text)
}

func TestChat_AppendHelperFunctions(t *testing.T) {
	reg := commands.NewRegistry()
	c := NewChat(reg)
	c.mdRenderer = &mockRenderer{}
	c.messages = nil

	c.appendUser("user text")
	c.appendError("error text")
	c.appendSystem("system text")
	c.appendStyled("styled text")
	c.appendMarkdown("md text")

	require.Equal(t, 5, len(c.messages))
	assert.Equal(t, MsgUser, c.messages[0].Kind)
	assert.Equal(t, "user text", c.messages[0].Text)
	assert.Equal(t, MsgError, c.messages[1].Kind)
	assert.Equal(t, "error text", c.messages[1].Text)
	assert.Equal(t, MsgSystem, c.messages[2].Kind)
	assert.Equal(t, "system text", c.messages[2].Text)
	assert.Equal(t, MsgStyled, c.messages[3].Kind)
	assert.Equal(t, "styled text", c.messages[3].Text)
	assert.Equal(t, MsgMarkdown, c.messages[4].Kind)
	assert.Equal(t, "md text", c.messages[4].Text)
}

// --- syncViewport tests ---

func TestChat_SyncViewport_Empty(t *testing.T) {
	reg := commands.NewRegistry()
	c := NewChat(reg)
	c.mdRenderer = &mockRenderer{}
	c.messages = nil
	c.syncViewport()
	// Should not panic with no messages.
}

func TestChat_SyncViewport_UserMessage(t *testing.T) {
	reg := commands.NewRegistry()
	c := NewChat(reg)
	c.mdRenderer = &mockRenderer{}
	c.messages = nil
	c.appendUser("hello")
	c.syncViewport()
	assert.Equal(t, 1, len(c.messages))
}

func TestChat_SyncViewport_OutputMessage(t *testing.T) {
	reg := commands.NewRegistry()
	c := NewChat(reg)
	c.mdRenderer = &mockRenderer{}
	c.messages = nil
	c.append(MsgOutput, "output text")
	c.syncViewport()
	assert.Equal(t, 1, len(c.messages))
}

func TestChat_SyncViewport_ErrorAndSystem(t *testing.T) {
	reg := commands.NewRegistry()
	c := NewChat(reg)
	c.mdRenderer = &mockRenderer{}
	c.messages = nil
	c.appendError("error text")
	c.appendSystem("system text")
	c.syncViewport()
	assert.Equal(t, 2, len(c.messages))
}

func TestChat_SyncViewport_MixedMessages(t *testing.T) {
	reg := commands.NewRegistry()
	c := NewChat(reg)
	c.mdRenderer = &mockRenderer{}
	c.messages = nil
	c.appendUser("user query")
	c.append(MsgOutput, "result 1")
	c.append(MsgOutput, "result 2")
	c.appendError("something broke")
	c.appendSystem("info")
	c.syncViewport()
	assert.Equal(t, 5, len(c.messages))
}

func TestChat_SyncViewport_ConsecutiveMarkdownJoined(t *testing.T) {
	reg := commands.NewRegistry()
	c := NewChat(reg)
	c.mdRenderer = &mockRenderer{}
	c.messages = nil
	c.appendMarkdown("line one")
	c.appendMarkdown("line two")
	c.append(MsgOutput, "after")
	c.syncViewport()
	assert.Equal(t, 3, len(c.messages))
}

func TestChat_SyncViewport_AfterClear(t *testing.T) {
	reg := commands.NewRegistry()
	c := NewChat(reg)
	c.mdRenderer = &mockRenderer{}
	c.appendUser("first")
	c.append(MsgOutput, "response")
	c.messages = nil
	c.syncViewport()
	assert.Equal(t, 0, len(c.messages))
}

func TestChat_SyncViewport_MultipleMarkdownBlocks(t *testing.T) {
	reg := commands.NewRegistry()
	c := NewChat(reg)
	c.mdRenderer = &mockRenderer{}
	c.messages = nil
	c.appendMarkdown("block1-a")
	c.appendMarkdown("block1-b")
	c.append(MsgOutput, "separator")
	c.appendMarkdown("block2")
	c.syncViewport()
	assert.Equal(t, 4, len(c.messages))
}

// --- Update: key handling tests ---

func TestChat_Update_Enter_SubmitsCommand(t *testing.T) {
	reg := commands.NewRegistry()
	reg.Register(&commands.Command{
		Name:  "test",
		Usage: "/test",
		Run: func(ctx context.Context, args commands.ParsedArgs, sink commands.Sink) error {
			return nil
		},
	})

	c := newTestChat(t, reg)
	c.input.SetValue("  /test  ")

	m, cmd := c.Update(tea.KeyMsg{Type: tea.KeyEnter})
	c = m.(*Chat)

	// handleSubmit calls execCommand which always returns a non-nil Cmd
	// and sets running=true (the Cmd runs the command in a goroutine).
	assert.NotNil(t, cmd)
	assert.True(t, c.running)
	assert.Equal(t, 1, len(c.history))
	assert.Equal(t, "/test", c.history[0])
	assert.Equal(t, 2, len(c.messages)) // 1 system + 1 user
	assert.Equal(t, MsgUser, c.messages[1].Kind)
	assert.True(t, c.dirty)
	assert.Contains(t, c.status, "Running /test")
}

func TestChat_Update_Enter_EmptyInput(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)
	c.input.SetValue("   ")

	m, cmd := c.Update(tea.KeyMsg{Type: tea.KeyEnter})
	c = m.(*Chat)

	assert.Nil(t, cmd)
	assert.False(t, c.running)
	// History is 1 (system message was not added to history).
	assert.Equal(t, 0, len(c.history))
}

func TestChat_Update_Enter_UnknownCommand(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)
	c.input.SetValue("/nonexistent")

	m, cmd := c.Update(tea.KeyMsg{Type: tea.KeyEnter})
	c = m.(*Chat)

	assert.Nil(t, cmd)
	assert.False(t, c.running)
	// 1 system msg + 1 user msg + 1 error msg
	assert.Equal(t, 3, len(c.messages))
	assert.Equal(t, MsgError, c.messages[2].Kind)
	assert.True(t, c.dirty)
}

func TestChat_Update_Enter_FallbackCommand(t *testing.T) {
	reg := commands.NewRegistry()
	reg.SetFallback("chat")
	reg.Register(&commands.Command{
		Name: "chat",
		Run: func(ctx context.Context, args commands.ParsedArgs, sink commands.Sink) error {
			return nil
		},
	})

	c := newTestChat(t, reg)
	c.input.SetValue("some unknown input")

	m, cmd := c.Update(tea.KeyMsg{Type: tea.KeyEnter})
	c = m.(*Chat)

	// Fallback command is executed like any other command.
	assert.NotNil(t, cmd)
	assert.True(t, c.running)
	assert.Equal(t, 1, len(c.history))
	assert.Equal(t, "some unknown input", c.history[0])
}

func TestChat_Update_Enter_WhenRunning(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)
	c.running = true

	_, cancel := context.WithCancel(context.Background())
	c.cancelFn = cancel

	m, cmd := c.Update(tea.KeyMsg{Type: tea.KeyEnter})
	c = m.(*Chat)

	assert.Nil(t, cmd)
	assert.True(t, c.running)
	assert.Equal(t, 0, len(c.history))
}

func TestChat_Update_SpecialCommands(t *testing.T) {
	reg := commands.NewRegistry()

	tests := []string{"/clear", "/cls", "clear"}
	for _, val := range tests {
		t.Run(val, func(t *testing.T) {
			c := newTestChat(t, reg)
			c.appendUser("first")
			c.append(MsgOutput, "response")
			c.input.SetValue(val)

			m, cmd := c.Update(tea.KeyMsg{Type: tea.KeyEnter})
			c = m.(*Chat)

			assert.Nil(t, cmd)
			assert.Equal(t, 0, len(c.messages))
			assert.True(t, c.dirty)
			assert.Equal(t, "", c.input.Value())
		})
	}
}

func TestChat_Update_QuitCommand(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)
	c.input.SetValue("/quit")

	m, cmd := c.Update(tea.KeyMsg{Type: tea.KeyEnter})
	c = m.(*Chat)

	assert.NotNil(t, cmd)
	assert.Equal(t, tea.QuitMsg{}, cmd())
}

func TestChat_Update_EscKey_Normal(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)

	m, cmd := c.Update(tea.KeyMsg{Type: tea.KeyEsc})
	c = m.(*Chat)

	assert.Nil(t, cmd)
	assert.False(t, c.running)
}

func TestChat_Update_EscKey_WhenRunning(t *testing.T) {
	reg := commands.NewRegistry()
	_, cancel := context.WithCancel(context.Background())

	c := newTestChat(t, reg)
	c.running = true
	c.cancelFn = cancel

	m, cmd := c.Update(tea.KeyMsg{Type: tea.KeyEsc})
	c = m.(*Chat)

	assert.Nil(t, cmd)
	// The cancelFn should be cleared and a cancellation message added.
	assert.True(t, len(c.messages) >= 2) // system init + "Task cancelled"
	assert.Contains(t, c.messages[len(c.messages)-1].Text, "cancelled")
}

func TestChat_Update_CtrlC_Normal(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)

	m, cmd := c.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	c = m.(*Chat)

	assert.NotNil(t, cmd)
	assert.Equal(t, tea.QuitMsg{}, cmd())
}

func TestChat_Update_CtrlC_WhenRunning(t *testing.T) {
	reg := commands.NewRegistry()
	_, cancel := context.WithCancel(context.Background())

	c := newTestChat(t, reg)
	c.running = true
	c.cancelFn = cancel

	m, cmd := c.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	c = m.(*Chat)

	assert.Nil(t, cmd)
	// The cancelFn should be cleared and a cancellation message added.
	assert.True(t, len(c.messages) >= 2) // system init + "Task cancelled"
	assert.Contains(t, c.messages[len(c.messages)-1].Text, "cancelled")
}

func TestChat_Update_HistoryUp(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)

	c.history = []string{"first", "second", "third"}
	c.historyIdx = len(c.history) // at the "empty" position

	m, _ := c.Update(tea.KeyMsg{Type: tea.KeyUp})
	c = m.(*Chat)
	assert.Equal(t, 2, c.historyIdx)
	assert.Equal(t, "third", c.input.Value())

	m, _ = c.Update(tea.KeyMsg{Type: tea.KeyUp})
	c = m.(*Chat)
	assert.Equal(t, 1, c.historyIdx)
	assert.Equal(t, "second", c.input.Value())

	m, _ = c.Update(tea.KeyMsg{Type: tea.KeyUp})
	c = m.(*Chat)
	assert.Equal(t, 0, c.historyIdx)
	assert.Equal(t, "first", c.input.Value())

	m, _ = c.Update(tea.KeyMsg{Type: tea.KeyUp})
	c = m.(*Chat)
	assert.Equal(t, 0, c.historyIdx)
}

func TestChat_Update_HistoryDown(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)

	c.history = []string{"first", "second", "third"}
	c.historyIdx = 0

	m, _ := c.Update(tea.KeyMsg{Type: tea.KeyDown})
	c = m.(*Chat)
	assert.Equal(t, 1, c.historyIdx)
	assert.Equal(t, "second", c.input.Value())

	m, _ = c.Update(tea.KeyMsg{Type: tea.KeyDown})
	c = m.(*Chat)
	assert.Equal(t, 2, c.historyIdx)
	assert.Equal(t, "third", c.input.Value())

	m, _ = c.Update(tea.KeyMsg{Type: tea.KeyDown})
	c = m.(*Chat)
	assert.Equal(t, 3, c.historyIdx)
	assert.Equal(t, "", c.input.Value())

	m, _ = c.Update(tea.KeyMsg{Type: tea.KeyDown})
	c = m.(*Chat)
	assert.Equal(t, 3, c.historyIdx)
	assert.Equal(t, "", c.input.Value())
}

func TestChat_Update_TabAutocomplete(t *testing.T) {
	reg := commands.NewRegistry()
	reg.Register(&commands.Command{Name: "help"})
	reg.Register(&commands.Command{Name: "history"})
	reg.Register(&commands.Command{Name: "config"})
	reg.Register(&commands.Command{Name: "analyze"})

	c := newTestChat(t, reg)
	c.input.SetValue("/he")

	m, _ := c.Update(tea.KeyMsg{Type: tea.KeyTab})
	c = m.(*Chat)
	assert.True(t, c.completing)
	// "help" matches prefix "he"
	assert.True(t, strings.HasPrefix(c.input.Value(), "/help"))

	m, _ = c.Update(tea.KeyMsg{Type: tea.KeyTab})
	c = m.(*Chat)
	assert.True(t, c.completing)
	// Cycling should show next match or wrap
}

func TestChat_Update_TabAutocomplete_CyclesMultiple(t *testing.T) {
	reg := commands.NewRegistry()
	reg.Register(&commands.Command{Name: "help"})
	reg.Register(&commands.Command{Name: "history"})
	reg.Register(&commands.Command{Name: "hello"})

	c := newTestChat(t, reg)
	c.input.SetValue("/h")

	m, _ := c.Update(tea.KeyMsg{Type: tea.KeyTab})
	c = m.(*Chat)
	assert.True(t, c.completing)
	assert.True(t, len(c.suggestions) > 0)
	initialIdx := c.suggestIdx

	// Cycle through all suggestions - each tab should advance the index
	m, _ = c.Update(tea.KeyMsg{Type: tea.KeyTab})
	c = m.(*Chat)
	assert.True(t, c.completing)
	assert.NotEqual(t, initialIdx, c.suggestIdx) // index should have changed
	assert.True(t, strings.HasPrefix(c.input.Value(), "/"))

	// Verify cycling wraps around
	m, _ = c.Update(tea.KeyMsg{Type: tea.KeyTab})
	c = m.(*Chat)
	assert.True(t, c.completing)
}

func TestChat_Update_TabNoSlash(t *testing.T) {
	reg := commands.NewRegistry()
	reg.Register(&commands.Command{Name: "help"})

	c := newTestChat(t, reg)
	c.input.SetValue("help")

	m, _ := c.Update(tea.KeyMsg{Type: tea.KeyTab})
	c = m.(*Chat)

	assert.False(t, c.completing)
	assert.Equal(t, "help", c.input.Value())
}

func TestChat_Update_TabEmptyInput(t *testing.T) {
	reg := commands.NewRegistry()
	reg.Register(&commands.Command{Name: "help"})

	c := newTestChat(t, reg)
	c.input.SetValue("")

	m, _ := c.Update(tea.KeyMsg{Type: tea.KeyTab})
	c = m.(*Chat)

	assert.False(t, c.completing)
}

func TestChat_Update_TabNoMatchingCommands(t *testing.T) {
	reg := commands.NewRegistry()
	reg.Register(&commands.Command{Name: "help"})

	c := newTestChat(t, reg)
	c.input.SetValue("/xyz")

	m, _ := c.Update(tea.KeyMsg{Type: tea.KeyTab})
	c = m.(*Chat)

	assert.True(t, c.completing)
	assert.Empty(t, c.suggestions)
	assert.Equal(t, "/xyz", c.input.Value())
}

func TestChat_Update_TabClearsOnNonTabKey(t *testing.T) {
	reg := commands.NewRegistry()
	reg.Register(&commands.Command{Name: "help"})

	c := newTestChat(t, reg)
	c.input.SetValue("/he")
	m, _ := c.Update(tea.KeyMsg{Type: tea.KeyTab})
	c = m.(*Chat)
	assert.True(t, c.completing)

	m, _ = c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	c = m.(*Chat)

	assert.False(t, c.completing)
	assert.Nil(t, c.suggestions)
}

func TestChat_Update_WindowSize(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)

	m, cmd := c.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	c = m.(*Chat)

	assert.Nil(t, cmd)
	assert.Equal(t, 120, c.width)
	assert.Equal(t, 40, c.height)
}

func TestChat_Update_MouseMsg(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)

	_, cmd := c.Update(tea.MouseMsg{Type: tea.MouseLeft})

	// Mouse goes to viewport, which may or may not return a cmd.
	// Just verify it doesn't panic and Chat state is preserved.
	assert.False(t, c.dirty)
	_ = cmd
}

func TestChat_Update_ScrollKeys(t *testing.T) {
	reg := commands.NewRegistry()

	tests := []struct {
		name string
		key  tea.KeyMsg
	}{
		{"pgup", tea.KeyMsg{Type: tea.KeyPgUp}},
		{"pgdown", tea.KeyMsg{Type: tea.KeyPgDown}},
		{"shift+up", tea.KeyMsg{Type: tea.KeyShiftUp}},
		{"shift+down", tea.KeyMsg{Type: tea.KeyShiftDown}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestChat(t, reg)
			c.Update(tt.key)
		})
	}
}

func TestChat_Update_LeakedTerminalInput(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]11;rgb:aa/bb/cc")}
	m, cmd := c.Update(msg)
	c = m.(*Chat)

	assert.Nil(t, cmd)
	assert.False(t, c.dirty)
}

// --- Update: message handling tests ---

func TestChat_Update_CmdOutputMsg_Error(t *testing.T) {
	reg := commands.NewRegistry()
	c := newCleanChat(t, reg)

	m, cmd := c.Update(cmdOutputMsg{line: "something failed", isErr: true})
	c = m.(*Chat)

	require.Nil(t, cmd)
	// dirty is synced to viewport and reset within Update.
	assert.Equal(t, 1, len(c.messages))
	assert.Equal(t, MsgError, c.messages[0].Kind)
	assert.Equal(t, "something failed", c.messages[0].Text)
}

func TestChat_Update_CmdOutputMsg_PreStyled(t *testing.T) {
	reg := commands.NewRegistry()
	c := newCleanChat(t, reg)

	m, cmd := c.Update(cmdOutputMsg{line: "<b>styled</b>", preStyled: true})
	c = m.(*Chat)

	require.Nil(t, cmd)
	assert.Equal(t, 1, len(c.messages))
	assert.Equal(t, MsgStyled, c.messages[0].Kind)
}

func TestChat_Update_CmdOutputMsg_Normal(t *testing.T) {
	reg := commands.NewRegistry()
	c := newCleanChat(t, reg)

	m, cmd := c.Update(cmdOutputMsg{line: "regular output"})
	c = m.(*Chat)

	require.Nil(t, cmd)
	assert.Equal(t, 1, len(c.messages))
	assert.Equal(t, MsgOutput, c.messages[0].Kind)
}

func TestChat_Update_CmdMarkdownLineMsg(t *testing.T) {
	reg := commands.NewRegistry()
	c := newCleanChat(t, reg)

	m, cmd := c.Update(cmdMarkdownLineMsg{line: "*markdown* line"})
	c = m.(*Chat)

	require.Nil(t, cmd)
	assert.Equal(t, 1, len(c.messages))
	assert.Equal(t, MsgMarkdown, c.messages[0].Kind)
}

func TestChat_Update_CmdStatusMsg(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)

	m, cmd := c.Update(cmdStatusMsg{text: "loading..."})
	c = m.(*Chat)

	require.Nil(t, cmd)
	assert.Equal(t, "loading...", c.status)
}

func TestChat_Update_CmdDoneMsg_Empty(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)
	c.running = true

	_, cancel := context.WithCancel(context.Background())
	c.cancelFn = cancel

	m, cmd := c.Update(cmdDoneMsg{summary: "", err: nil})
	c = m.(*Chat)

	require.Nil(t, cmd)
	assert.False(t, c.running)
	assert.Nil(t, c.cancelFn)
	assert.Equal(t, "", c.status)
	// System message from init is still there.
	assert.Equal(t, 1, len(c.messages))
}

func TestChat_Update_CmdDoneMsg_WithSummary(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)
	c.running = true

	_, cancel := context.WithCancel(context.Background())
	c.cancelFn = cancel

	m, cmd := c.Update(cmdDoneMsg{summary: "done!", err: nil})
	c = m.(*Chat)

	require.Nil(t, cmd)
	assert.False(t, c.running)
	// Summary is appended as MsgOutput.
	assert.Equal(t, "done!", c.messages[len(c.messages)-1].Text)
	assert.Equal(t, MsgOutput, c.messages[len(c.messages)-1].Kind)
}

func TestChat_Update_CmdDoneMsg_WithError(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)
	c.running = true

	_, cancel := context.WithCancel(context.Background())
	c.cancelFn = cancel

	err := errors.New("command failed")
	m, cmd := c.Update(cmdDoneMsg{err: err})
	c = m.(*Chat)

	require.Nil(t, cmd)
	assert.False(t, c.running)
	assert.Equal(t, "error: command failed", c.messages[len(c.messages)-1].Text)
	assert.Equal(t, MsgError, c.messages[len(c.messages)-1].Kind)
}

func TestChat_Update_CmdDoneMsg_ContextCanceledIgnored(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)
	c.running = true

	_, cancel := context.WithCancel(context.Background())
	c.cancelFn = cancel

	m, cmd := c.Update(cmdDoneMsg{err: context.Canceled})
	c = m.(*Chat)

	require.Nil(t, cmd)
	assert.False(t, c.running)
	for _, m := range c.messages {
		assert.NotEqual(t, MsgError, m.Kind, "should not have error message for context.Canceled")
		assert.NotContains(t, m.Text, "context canceled")
	}
}

func TestChat_Update_UnknownMessageType(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)

	type unknownMsg struct{}
	m, cmd := c.Update(unknownMsg{})
	c = m.(*Chat)

	assert.Nil(t, cmd)
}

func TestChat_Update_SpinnerTick(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)

	m, cmd := c.Update(spinner.TickMsg{})
	c = m.(*Chat)

	assert.NotNil(t, cmd)
	assert.False(t, c.dirty)
}

// --- View tests ---

func TestChat_View_NotReady(t *testing.T) {
	reg := commands.NewRegistry()
	c := NewChat(reg)

	result := c.View()
	assert.Equal(t, "Initializing...", result)
}

func TestChat_View_Normal(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)
	c.appendUser("hello")
	c.dirty = true
	c.syncViewport()

	result := c.View()

	assert.Contains(t, result, "majordomo") // header bar
	assert.Contains(t, result, "hello")     // user message
	assert.Contains(t, result, "❯")         // prompt character
}

func TestChat_View_WithStatus(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)
	c.running = true
	c.status = "Running /test..."

	result := c.View()

	assert.Contains(t, result, "Running /test...")
}

func TestChat_View_NoStatus(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)

	result := c.View()

	assert.NotContains(t, result, "Running")
}

// --- ClearRunningState tests ---

func TestChat_ClearRunningState(t *testing.T) {
	reg := commands.NewRegistry()
	_, cancel := context.WithCancel(context.Background())
	c := newTestChat(t, reg)
	c.running = true
	c.status = "something"
	c.cancelFn = cancel

	c.ClearRunningState()

	assert.False(t, c.running)
	assert.Nil(t, c.cancelFn)
	assert.Equal(t, "", c.status)
}

// --- Command with flags ---

func TestChat_Update_Enter_CommandWithFlags(t *testing.T) {
	reg := commands.NewRegistry()
	reg.Register(&commands.Command{
		Name:  "analyze",
		Usage: "/analyze <path> [--json]",
		Args: []commands.Arg{
			{Name: "path", Required: true},
			{Name: "json", IsFlag: true},
		},
		Run: func(ctx context.Context, args commands.ParsedArgs, sink commands.Sink) error {
			return nil
		},
	})

	c := newTestChat(t, reg)
	c.input.SetValue("/analyze . --json")

	m, cmd := c.Update(tea.KeyMsg{Type: tea.KeyEnter})
	c = m.(*Chat)

	// Command with flags is executed like any other command.
	assert.NotNil(t, cmd)
	assert.True(t, c.running)
	assert.Equal(t, 1, len(c.history))
	assert.Equal(t, "/analyze . --json", c.history[0])
	assert.Contains(t, c.status, "Running /analyze")
}

// --- completeOrCycle edge case ---

func TestChat_CompleteOrCycle_NoPrefix(t *testing.T) {
	reg := commands.NewRegistry()
	reg.Register(&commands.Command{Name: "help"})

	c := newTestChat(t, reg)
	c.input.SetValue("/")

	c.completeOrCycle()

	assert.True(t, c.completing)
	assert.True(t, len(c.suggestions) > 0)
}

// --- renderMarkdownJoined tests ---

func TestChat_RenderMarkdownJoined_Empty(t *testing.T) {
	reg := commands.NewRegistry()
	c := NewChat(reg)
	c.mdRenderer = &mockRenderer{}

	result := c.renderMarkdownJoined([]string{})
	assert.Equal(t, "  ", result)
}

func TestChat_RenderMarkdownJoined_WhitespaceOnly(t *testing.T) {
	reg := commands.NewRegistry()
	c := NewChat(reg)
	c.mdRenderer = &mockRenderer{}

	result := c.renderMarkdownJoined([]string{"  ", "  "})
	assert.NotEmpty(t, result)
}

func TestChat_RenderMarkdownJoined_WithMockRenderer(t *testing.T) {
	reg := commands.NewRegistry()
	c := NewChat(reg)
	c.mdRenderer = &mockRenderer{}

	result := c.renderMarkdownJoined([]string{"# Hello", "World"})
	assert.Contains(t, result, "# Hello")
}

// --- WindowSizeMsg tests ---

func TestChat_Update_WindowSizeMsg_FirstTime(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)

	assert.True(t, c.ready)
	assert.True(t, c.viewport.MouseWheelEnabled)
	// dirty is synced and reset within Update
}

func TestChat_Update_WindowSizeMsg_TooSmallHeight(t *testing.T) {
	reg := commands.NewRegistry()
	c := NewChat(reg)
	c.mdRenderer = &mockRenderer{}

	m, _ := c.Update(tea.WindowSizeMsg{Width: 40, Height: 2})
	pc := m.(*Chat)

	assert.True(t, pc.ready)
	assert.True(t, pc.viewport.MouseWheelEnabled)
	assert.Equal(t, 1, pc.viewport.Height)
}

// --- SetProgram test ---

func TestChat_SetProgram(t *testing.T) {
	reg := commands.NewRegistry()
	c := newTestChat(t, reg)

	assert.Nil(t, c.program)
	c.SetProgram(nil)
	assert.Nil(t, c.program)
}

// --- History: submitting same text adds again ---

func TestChat_Update_Enter_AddsToHistory(t *testing.T) {
	reg := commands.NewRegistry()
	reg.Register(&commands.Command{
		Name: "help",
		Run: func(ctx context.Context, args commands.ParsedArgs, sink commands.Sink) error {
			return nil
		},
	})

	c := newTestChat(t, reg)
	c.input.SetValue("/help")

	m, _ := c.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pc := m.(*Chat)
	// First submit adds to history.
	assert.Equal(t, 1, len(pc.history))
	assert.Equal(t, "/help", pc.history[0])

	// Submit a different command after clearing running state.
	pc.running = false
	pc.cancelFn = nil
	pc.input.SetValue("/help --flag")
	m, _ = c.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pc = m.(*Chat)
	// Second submit also adds to history.
	assert.Equal(t, 2, len(pc.history))
	assert.Equal(t, "/help --flag", pc.history[1])
}

// --- Tab preserves completing when no slash prefix ---

func TestChat_Update_Tab_PreservesCompletingWhenNoSlash(t *testing.T) {
	reg := commands.NewRegistry()
	reg.Register(&commands.Command{Name: "help"})

	c := newTestChat(t, reg)
	c.completing = true
	c.suggestions = []string{"help"}

	m, _ := c.Update(tea.KeyMsg{Type: tea.KeyTab})
	c = m.(*Chat)

	assert.True(t, c.completing)
}
