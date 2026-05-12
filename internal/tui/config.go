package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/julython/majordomo/internal/config"
)

type ConfigEditor struct {
	cfg      *config.Config
	inputs   []textinput.Model
	focused  int
	width    int
	height   int
	err      error
	saved    bool
	quitting bool
}

type configSavedMsg struct{}
type configCancelledMsg struct{}

var (
	focusedStyle = lipgloss.NewStyle().Foreground(purple).Bold(true)
	blurredStyle = lipgloss.NewStyle().Foreground(dim)
	labelStyle   = lipgloss.NewStyle().Foreground(white).Width(20)
	titleStyle   = lipgloss.NewStyle().Foreground(purple).Bold(true).Padding(1, 0)
	helpStyle    = lipgloss.NewStyle().Foreground(dim).Italic(true).Padding(1, 0)
	buttonStyle  = lipgloss.NewStyle().
			Foreground(white).
			Background(purple).
			Padding(0, 3).
			Margin(0, 1).
			Bold(true)
	buttonBlurredStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#9CA3AF")).
				Background(lipgloss.Color("#374151")).
				Padding(0, 3).
				Margin(0, 1)
)

func NewConfigEditor() (*ConfigEditor, error) {
	cfg, err := config.Load("")
	if err != nil {
		cfg = config.Default()
	}

	inputs := make([]textinput.Model, 4)

	// Server URL
	inputs[0] = textinput.New()
	inputs[0].Placeholder = "https://julython.org"
	inputs[0].SetValue(cfg.Server.URL)
	inputs[0].CharLimit = 200
	inputs[0].Width = 50

	// LLM Provider
	inputs[1] = textinput.New()
	inputs[1].Placeholder = "auto"
	inputs[1].SetValue(cfg.LLM.Provider)
	inputs[1].CharLimit = 50
	inputs[1].Width = 50

	// LLM Model
	inputs[2] = textinput.New()
	inputs[2].Placeholder = "llama3.2"
	inputs[2].SetValue(cfg.LLM.Model)
	inputs[2].CharLimit = 100
	inputs[2].Width = 50

	// LLM URL
	inputs[3] = textinput.New()
	inputs[3].Placeholder = "http://localhost:11434"
	inputs[3].SetValue(cfg.LLM.URL)
	inputs[3].CharLimit = 200
	inputs[3].Width = 50

	inputs[0].Focus()

	return &ConfigEditor{
		cfg:     cfg,
		inputs:  inputs,
		focused: 0,
	}, nil
}

func (c *ConfigEditor) Init() tea.Cmd {
	return textinput.Blink
}

func (c *ConfigEditor) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.width = msg.Width
		c.height = msg.Height

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			c.quitting = true
			return c, func() tea.Msg { return configCancelledMsg{} }

		case "tab", "down":
			c.nextField()
			return c, nil

		case "shift+tab", "up":
			c.prevField()
			return c, nil

		case "enter":
			if c.focused == len(c.inputs) {
				// Save button
				return c, c.save()
			} else if c.focused == len(c.inputs)+1 {
				// Cancel button
				c.quitting = true
				return c, func() tea.Msg { return configCancelledMsg{} }
			}
			// On a field, just move to next
			c.nextField()
			return c, nil
		}
	}

	// Update the focused input
	if c.focused < len(c.inputs) {
		var cmd tea.Cmd
		c.inputs[c.focused], cmd = c.inputs[c.focused].Update(msg)
		return c, cmd
	}

	return c, nil
}

func (c *ConfigEditor) View() string {
	if c.quitting {
		return ""
	}

	var b strings.Builder

	// Title
	b.WriteString(titleStyle.Render("⚙  Configuration"))
	b.WriteString("\n\n")

	// Fields
	labels := []string{
		"Server URL:",
		"LLM Provider:",
		"LLM Model:",
		"LLM URL:",
	}

	for i, input := range c.inputs {
		label := labelStyle.Render(labels[i])
		if c.focused == i {
			label = focusedStyle.Render("▸ " + labels[i])
		} else {
			label = "  " + label
		}

		b.WriteString(label)
		b.WriteString(" ")

		if c.focused == i {
			b.WriteString(input.View())
		} else {
			b.WriteString(blurredStyle.Render(input.Value()))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")

	// Buttons
	saveBtn := buttonBlurredStyle.Render("Save")
	if c.focused == len(c.inputs) {
		saveBtn = buttonStyle.Render("Save")
	}

	cancelBtn := buttonBlurredStyle.Render("Cancel")
	if c.focused == len(c.inputs)+1 {
		cancelBtn = buttonStyle.Render("Cancel")
	}

	b.WriteString("  ")
	b.WriteString(saveBtn)
	b.WriteString(cancelBtn)
	b.WriteString("\n\n")

	// Help
	b.WriteString(helpStyle.Render("  ↑↓/tab: navigate • enter: select • esc: cancel"))

	if c.err != nil {
		b.WriteString("\n\n")
		b.WriteString(errorStyle.Render(fmt.Sprintf("  Error: %v", c.err)))
	}

	return b.String()
}

func (c *ConfigEditor) nextField() {
	if c.focused < len(c.inputs) {
		c.inputs[c.focused].Blur()
	}
	c.focused = (c.focused + 1) % (len(c.inputs) + 2)
	if c.focused < len(c.inputs) {
		c.inputs[c.focused].Focus()
	}
}

func (c *ConfigEditor) prevField() {
	if c.focused < len(c.inputs) {
		c.inputs[c.focused].Blur()
	}
	c.focused--
	if c.focused < 0 {
		c.focused = len(c.inputs) + 1
	}
	if c.focused < len(c.inputs) {
		c.inputs[c.focused].Focus()
	}
}

func (c *ConfigEditor) save() tea.Cmd {
	return func() tea.Msg {
		// Update config from inputs
		c.cfg.Server.URL = c.inputs[0].Value()
		c.cfg.LLM.Provider = c.inputs[1].Value()
		c.cfg.LLM.Model = c.inputs[2].Value()
		c.cfg.LLM.URL = c.inputs[3].Value()

		// Save to disk
		if err := config.Save(c.cfg); err != nil {
			c.err = err
			return nil
		}

		c.saved = true
		c.quitting = true
		return configSavedMsg{}
	}
}
