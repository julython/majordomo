package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/julython/majordomo/internal/commands"
	"github.com/julython/majordomo/internal/mdrender"
)

// --- Messages ---

type (
	cmdOutputMsg struct {
		line      string
		isErr     bool
		preStyled bool
	}
	cmdStatusMsg struct{ text string }
	cmdDoneMsg   struct {
		summary string
		err     error
	}
	cmdMarkdownLineMsg struct{ line string }
)

// --- Chat message types ---

type MsgKind int

const (
	MsgUser MsgKind = iota
	MsgOutput
	MsgMarkdown
	MsgStyled
	MsgError
	MsgSystem
)

type ChatMsg struct {
	Kind MsgKind
	Text string
	Time time.Time
}

// --- Styles ---

var (
	purple = lipgloss.Color("#7C3AED")
	red    = lipgloss.Color("#EF4444")
	yellow = lipgloss.Color("#F59E0B")
	dim    = lipgloss.Color("#6B7280")
	white  = lipgloss.Color("#F9FAFB")

	promptStyle = lipgloss.NewStyle().Foreground(purple).Bold(true)
	userStyle   = lipgloss.NewStyle().Foreground(white).Bold(true)
	outputStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#D1D5DB"))
	errorStyle  = lipgloss.NewStyle().Foreground(red)
	systemStyle = lipgloss.NewStyle().Foreground(dim).Italic(true)
	statusLine  = lipgloss.NewStyle().Foreground(yellow)
	barStyle    = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9CA3AF")).
			Background(lipgloss.Color("#1F2937")).
			Padding(0, 1)
)

// --- Chat model ---

type Chat struct {
	registry *commands.Registry
	input    textinput.Model
	viewport viewport.Model
	spinner  spinner.Model

	messages []ChatMsg
	dirty    bool
	status   string
	running  bool

	cancelFn context.CancelFunc
	program  *tea.Program

	// Autocomplete
	suggestions []string
	suggestIdx  int
	completing  bool

	// History
	history    []string
	historyIdx int

	width  int
	height int
	ready  bool

	mdRenderer markdownTermRenderer
	mdWrap     int
}

type markdownTermRenderer interface {
	Render(src string) (string, error)
}

func NewChat(reg *commands.Registry) Chat {
	ti := textinput.New()
	ti.Placeholder = "Type /help or a command..."
	ti.Prompt = "❯ "
	ti.PromptStyle = promptStyle
	ti.Focus()
	ti.CharLimit = 500

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(purple)

	return Chat{
		registry:   reg,
		input:      ti,
		spinner:    s,
		historyIdx: -1,
	}
}

func (c *Chat) SetProgram(p *tea.Program) {
	c.program = p
}

func (c *Chat) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
		c.spinner.Tick,
	)
}

func (c *Chat) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		c.width = msg.Width
		c.height = msg.Height

		vpH := c.height - 4
		if vpH < 1 {
			vpH = 1
		}

		if !c.ready {
			c.viewport = viewport.New(c.width, vpH)
			c.viewport.MouseWheelEnabled = true
			c.appendSystem("🏠 majordomo — type /help to get started")
			c.dirty = true
			c.ready = true
		} else {
			c.viewport.Width = c.width
			c.viewport.Height = vpH
			c.dirty = true
		}
		c.input.Width = c.width - 4

	// Mouse events go ONLY to viewport — never to input
	case tea.MouseMsg:
		var cmd tea.Cmd
		c.viewport, cmd = c.viewport.Update(msg)
		return c, cmd

	case tea.KeyMsg:
		key := msg.String()

		// Clear autocomplete on any non-tab key
		if key != "tab" && c.completing {
			c.completing = false
			c.suggestions = nil
		}

		switch key {
		case "ctrl+c":
			if c.running && c.cancelFn != nil {
				c.cancelFn()
				c.appendSystem("⊘ Task cancelled")
				c.syncViewport()
				c.viewport.GotoBottom()
				return c, nil
			}
			return c, tea.Quit

		case "esc":
			if c.running && c.cancelFn != nil {
				c.cancelFn()
				c.appendSystem("⊘ Task cancelled")
				c.syncViewport()
				c.viewport.GotoBottom()
				return c, nil
			}
			return c, nil

		case "enter":
			return c, c.handleSubmit()

		case "tab":
			c.completeOrCycle()
			return c, nil

		case "up":
			if c.historyIdx > 0 {
				c.historyIdx--
				c.input.SetValue(c.history[c.historyIdx])
				c.input.CursorEnd()
			}
			return c, nil

		case "down":
			if c.historyIdx < len(c.history)-1 {
				c.historyIdx++
				c.input.SetValue(c.history[c.historyIdx])
				c.input.CursorEnd()
			} else {
				c.historyIdx = len(c.history)
				c.input.SetValue("")
			}
			return c, nil

		// All scroll keys → viewport only
		case "pgup", "pgdown":
			var cmd tea.Cmd
			c.viewport, cmd = c.viewport.Update(msg)
			return c, cmd

		// Shift+up/down for line-by-line scroll
		case "shift+up":
			c.viewport.LineUp(1)
			return c, nil
		case "shift+down":
			c.viewport.LineDown(1)
			return c, nil

		default:
			if isLeakedTerminalInput(msg) {
				return c, nil
			}
			// Regular typing → input only, nothing else
			var cmd tea.Cmd
			c.input, cmd = c.input.Update(msg)
			return c, cmd
		}

	case cmdOutputMsg:
		if msg.isErr {
			c.appendError(msg.line)
		} else if msg.preStyled {
			c.appendStyled(msg.line)
		} else {
			c.append(MsgOutput, msg.line)
		}
		c.dirty = true

	case cmdMarkdownLineMsg:
		c.appendMarkdown(msg.line)
		c.dirty = true

	case cmdStatusMsg:
		c.status = msg.text

	case cmdDoneMsg:
		c.running = false
		c.cancelFn = nil
		c.status = ""
		if msg.summary != "" {
			c.append(MsgOutput, msg.summary)
		}
		if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
			c.appendError(fmt.Sprintf("error: %v", msg.err))
		}
		c.dirty = true

	case spinner.TickMsg:
		// Always keep the spinner ticking — if we skip a tick
		// the spinner stops permanently.
		var cmd tea.Cmd
		c.spinner, cmd = c.spinner.Update(msg)
		return c, cmd
	}

	// Sync viewport only when messages changed
	if c.dirty {
		c.syncViewport()
		c.viewport.GotoBottom()
		c.dirty = false
	}

	return c, tea.Batch(cmds...)
}

func (c *Chat) handleSubmit() tea.Cmd {
	val := strings.TrimSpace(c.input.Value())
	if val == "" {
		return nil
	}

	switch val {
	case "/clear", "/cls", "clear":
		c.input.SetValue("")
		c.suggestions = nil
		c.completing = false
		c.messages = nil
		c.dirty = true
		return nil
	case "/quit", "/exit", "/q":
		return tea.Quit
	}

	if c.running {
		// Another command is in flight; leave the line so the user can submit after it finishes.
		return nil
	}

	c.input.SetValue("")
	c.suggestions = nil
	c.completing = false

	c.history = append(c.history, val)
	c.historyIdx = len(c.history)

	c.appendUser(val)

	cmd, args, err := c.registry.Parse(val)
	if err != nil {
		c.appendError(err.Error())
		c.dirty = true
		return nil
	}

	c.running = true
	c.status = fmt.Sprintf("Running /%s...", cmd.Name)
	c.dirty = true

	ctx, cancel := context.WithCancel(context.Background())
	c.cancelFn = cancel

	return c.execCommand(ctx, cmd, args)
}

func (c *Chat) View() string {
	if !c.ready {
		return "Initializing..."
	}

	header := barStyle.Width(c.width).Render("majordomo")

	var statusBar string
	if c.status != "" {
		statusBar = fmt.Sprintf(" %s %s", c.spinner.View(), statusLine.Render(c.status))
	}

	// Reset SGR after the viewport so lipgloss/glamour styles from chat output
	// cannot bleed into the status line or prompt (shows up as stray escape fragments).
	return fmt.Sprintf("%s\n%s%s\n%s\n%s%s",
		header,
		c.viewport.View(),
		ansi.ResetStyle,
		statusBar,
		c.input.View(),
		ansi.ResetStyle,
	)
}

// --- Rendering ---

func (c *Chat) syncViewport() {
	var lines []string
	for i := 0; i < len(c.messages); {
		m := c.messages[i]
		switch m.Kind {
		case MsgUser:
			lines = append(lines, promptStyle.Render("❯ ")+userStyle.Render(m.Text))
			i++
		case MsgOutput:
			lines = append(lines, "  "+outputStyle.Render(m.Text))
			i++
		case MsgStyled:
			lines = append(lines, "  "+m.Text)
			i++
		case MsgMarkdown:
			var chunk []string
			for i < len(c.messages) && c.messages[i].Kind == MsgMarkdown {
				chunk = append(chunk, c.messages[i].Text)
				i++
			}
			lines = append(lines, c.renderMarkdownJoined(chunk))
		case MsgError:
			lines = append(lines, "  "+errorStyle.Render(m.Text))
			i++
		case MsgSystem:
			lines = append(lines, "  "+systemStyle.Render(m.Text))
			i++
		}
	}
	c.viewport.SetContent(strings.Join(lines, "\n"))
}

func (c *Chat) append(kind MsgKind, text string) {
	c.messages = append(c.messages, ChatMsg{Kind: kind, Text: text, Time: time.Now()})
}

func (c *Chat) appendUser(text string)   { c.append(MsgUser, text) }
func (c *Chat) appendError(text string)  { c.append(MsgError, text) }
func (c *Chat) appendSystem(text string) { c.append(MsgSystem, text) }

func (c *Chat) appendStyled(text string) {
	c.messages = append(c.messages, ChatMsg{Kind: MsgStyled, Text: text, Time: time.Now()})
}

func (c *Chat) appendMarkdown(text string) {
	c.messages = append(c.messages, ChatMsg{Kind: MsgMarkdown, Text: text, Time: time.Now()})
}

func (c *Chat) renderMarkdownJoined(lines []string) string {
	src := strings.Join(lines, "\n")
	if strings.TrimSpace(src) == "" {
		return "  "
	}
	w := c.width - 4
	if w < 20 {
		w = 20
	}
	if c.mdRenderer == nil || c.mdWrap != w {
		r, err := mdrender.NewRenderer(w)
		if err != nil {
			c.mdRenderer = nil
			c.mdWrap = 0
			return mdrender.IndentEachLine("  ", outputStyle.Render(src))
		}
		c.mdRenderer = r
		c.mdWrap = w
	}
	out, err := c.mdRenderer.Render(src)
	if err != nil {
		return mdrender.IndentEachLine("  ", outputStyle.Render(src))
	}
	return mdrender.IndentEachLine("  ", out)
}

// isLeakedTerminalInput detects stdin garbage from OSC queries or SGR mouse
// sequences that Bubble Tea may surface as KeyRunes (must not reach textinput).
func isLeakedTerminalInput(msg tea.KeyMsg) bool {
	if msg.Type != tea.KeyRunes {
		return false
	}
	s := string(msg.Runes)
	switch {
	case strings.Contains(s, "]11;"), strings.Contains(s, "]10;"):
		return true
	case strings.Contains(s, "rgb:") && strings.Contains(s, "/"):
		return true
	case strings.Contains(s, "[<") && strings.Contains(s, ";") && strings.HasSuffix(s, "M"):
		return true
	default:
		return false
	}
}

// --- Command execution ---

func (c *Chat) execCommand(ctx context.Context, cmd *commands.Command, args commands.ParsedArgs) tea.Cmd {
	return func() tea.Msg {
		p := c.program
		if p == nil {
			return cmdDoneMsg{err: fmt.Errorf("no program reference")}
		}
		sink := NewStreamSink(p)
		// Run the command off the event loop so the UI keeps processing ticks
		// (spinner) and key events (Ctrl+C → context cancel).
		go func() {
			err := cmd.Run(ctx, args, sink)
			p.Send(cmdDoneMsg{err: err})
		}()
		return nil
	}
}

// --- Autocomplete ---

func (c *Chat) completeOrCycle() {
	val := c.input.Value()
	if val == "" || val[0] != '/' {
		return
	}

	prefix := strings.TrimPrefix(val, "/")
	prefix = strings.Split(prefix, " ")[0]

	if !c.completing {
		c.suggestions = nil
		for _, name := range c.registry.Names() {
			if strings.HasPrefix(name, prefix) {
				c.suggestions = append(c.suggestions, name)
			}
		}
		c.suggestIdx = 0
		c.completing = true
	} else {
		c.suggestIdx = (c.suggestIdx + 1) % len(c.suggestions)
	}

	if len(c.suggestions) > 0 {
		c.input.SetValue("/" + c.suggestions[c.suggestIdx] + " ")
		c.input.CursorEnd()
	}
}
