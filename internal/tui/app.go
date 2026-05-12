package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/julython/majordomo/internal/commands"
)

// AppMode represents the current mode of the application
type AppMode int

const (
	ModeChat AppMode = iota
	ModeConfig
)

// App is the main application model that can switch between modes
type App struct {
	mode         AppMode
	chat         *Chat
	configEditor *ConfigEditor
	registry     *commands.Registry
	width        int
	height       int
}

type switchToConfigMsg struct{}
type switchToChatMsg struct{}

func NewApp(reg *commands.Registry) *App {
	chat := NewChat(reg)
	return &App{
		mode:     ModeChat,
		chat:     &chat,
		registry: reg,
	}
}

func (a *App) SetProgram(p *tea.Program) {
	a.chat.SetProgram(p)
}

func (a *App) Init() tea.Cmd {
	return a.chat.Init()
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height

	case switchToConfigMsg:
		editor, err := NewConfigEditor()
		if err != nil {
			// Stay in chat mode, show error
			return a, nil
		}
		a.configEditor = editor
		a.mode = ModeConfig
		return a, a.configEditor.Init()

	case switchToChatMsg:
		a.mode = ModeChat
		a.configEditor = nil
		// Refresh chat display
		a.chat.dirty = true
		return a, nil

	case configSavedMsg:
		a.chat.appendSystem("✓ Configuration saved")
		a.mode = ModeChat
		a.configEditor = nil
		a.chat.ClearRunningState()
		a.chat.dirty = true
		return a, nil

	case configCancelledMsg:
		a.mode = ModeChat
		a.configEditor = nil
		a.chat.ClearRunningState()
		a.chat.dirty = true
		return a, nil

	case cmdDoneMsg:
		// Always route cmdDoneMsg to chat, even in config mode
		if a.mode == ModeConfig {
			// In config mode, just swallow it - we'll clear state on exit
			return a, nil
		}
		// Process in chat normally
		var cmd tea.Cmd
		m, cmd := a.chat.Update(msg)
		a.chat = m.(*Chat)
		return a, cmd
	}

	// Route to current mode
	switch a.mode {
	case ModeChat:
		var cmd tea.Cmd
		m, cmd := a.chat.Update(msg)
		a.chat = m.(*Chat)
		return a, cmd

	case ModeConfig:
		if a.configEditor != nil {
			var cmd tea.Cmd
			m, cmd := a.configEditor.Update(msg)
			a.configEditor = m.(*ConfigEditor)
			return a, cmd
		}
	}

	return a, nil
}

func (a *App) View() string {
	switch a.mode {
	case ModeConfig:
		if a.configEditor != nil {
			return a.configEditor.View()
		}
		return "Loading config editor..."

	case ModeChat:
		return a.chat.View()

	default:
		return "Unknown mode"
	}
}

// SwitchToConfig returns a command to switch to config mode
func SwitchToConfig() tea.Cmd {
	return func() tea.Msg {
		return switchToConfigMsg{}
	}
}
