package mdrender

import (
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

// TermWidth returns the terminal width or fallback if unknown.
func TermWidth(fallback int) int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		return fallback
	}
	return w
}

// NewRenderer builds a terminal markdown renderer with word wrap.
func NewRenderer(wordWrap int) (*glamour.TermRenderer, error) {
	if wordWrap < 20 {
		wordWrap = 20
	}
	cfg := terminalStyleConfig()
	return glamour.NewTermRenderer(
		glamour.WithStyles(cfg),
		glamour.WithWordWrap(wordWrap),
		glamour.WithColorProfile(termenv.DefaultOutput().ColorProfile()),
	)
}

// terminalStyleConfig mirrors glamour's style resolution (GLAMOUR_STYLE + auto),
// then tweaks inline code (backtick spans) so delimiters read clearly in the TUI.
func terminalStyleConfig() ansi.StyleConfig {
	name := os.Getenv("GLAMOUR_STYLE")
	if name == "" {
		name = styles.AutoStyle
	}
	if name != styles.AutoStyle {
		if cfg, ok := styles.DefaultStyles[name]; ok {
			c := *cfg
			enhanceInlineCodeStyle(&c)
			return c
		}
	}
	return terminalStyleAuto()
}

func terminalStyleAuto() ansi.StyleConfig {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		c := styles.NoTTYStyleConfig
		enhanceInlineCodeStyle(&c)
		return c
	}
	// Do not call termenv.HasDarkBackground() — it runs OSC 10/11 queries that read
	// stdin and race with Bubble Tea, leaking responses into the prompt and stalling
	// the UI until the next keypress.
	if envLightBackground() {
		c := styles.LightStyleConfig
		enhanceInlineCodeStyle(&c)
		return c
	}
	c := styles.DarkStyleConfig
	enhanceInlineCodeStyle(&c)
	return c
}

// envLightBackground uses COLORFGBG only (no OSC), matching common terminal behavior.
func envLightBackground() bool {
	bg := os.Getenv("COLORFGBG")
	if !strings.Contains(bg, ";") {
		return false
	}
	parts := strings.Split(bg, ";")
	last := strings.TrimSpace(parts[len(parts)-1])
	n, err := strconv.Atoi(last)
	if err != nil {
		return false
	}
	// In 16-color mode, background indices 7–15 are typically light.
	return n >= 7 && n <= 15
}

func enhanceInlineCodeStyle(c *ansi.StyleConfig) {
	p := c.Code.StylePrimitive
	p.Prefix = "`"
	p.Suffix = "`"
	p.Bold = boolPtr(true)
	c.Code.StylePrimitive = p
}

func boolPtr(b bool) *bool { return &b }

// IndentEachLine prefixes every line (for aligning with the chat gutter).
func IndentEachLine(prefix, s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return prefix
	}
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}
