package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Arg describes a single argument or flag for a command.
type Arg struct {
	Name        string
	Short       string // single char alias, e.g. "j" for --json
	Description string
	Required    bool
	Default     string
	IsFlag      bool // true = boolean flag, false = takes a value
}

// Command is a single thing majordomo can do.
// Both the CLI entrypoint and the TUI read from the same registry.
type Command struct {
	Name        string
	Aliases     []string
	Description string
	Usage       string // e.g. "/analyze <path> [--json]"
	Args        []Arg
	Category    string // "analysis", "worker", "model", "config"
	Hidden      bool   // internal commands not shown in /help

	// Run executes the command. It writes output to the Sink and
	// can be cancelled via context.
	Run func(ctx context.Context, args ParsedArgs, sink Sink) error
}

// ParsedArgs is the result of parsing user input against a command's arg spec.
type ParsedArgs struct {
	Positional []string
	Flags      map[string]string // --key=value or --flag (value="true")
	Raw        string            // original input
}

// Sink is how a command writes output back to whatever is rendering it.
// The CLI sink writes to stdout. The TUI sink appends chat messages.
type Sink interface {
	// Print adds a line of text output.
	Print(text string)
	// PrintMarkdown adds a line that is part of an LLM markdown response.
	// The UI may merge consecutive markdown lines and render them as one document.
	PrintMarkdown(text string)
	// PrintStyled is a line already styled with lipgloss or other ANSI (no extra TUI tint).
	PrintStyled(text string)
	// Status shows a transient status (spinner text in TUI, ignored in CLI).
	Status(text string)
	// Error marks the output as an error.
	Error(text string)
	// Finish signals the command is done. Pass a summary or "".
	Finish(summary string)
}

// Registry holds all known commands.
type Registry struct {
	commands map[string]*Command
	aliases  map[string]string
	fallback string // command name to use for unknown input
}

func NewRegistry() *Registry {
	return &Registry{
		commands: make(map[string]*Command),
		aliases:  make(map[string]string),
	}
}

// SetFallback sets the command name to use when input doesn't match any command.
// The full input is passed as a single positional arg.
func (r *Registry) SetFallback(name string) {
	r.fallback = name
}

func (r *Registry) Register(cmd *Command) {
	r.commands[cmd.Name] = cmd
	for _, alias := range cmd.Aliases {
		r.aliases[alias] = cmd.Name
	}
}

func (r *Registry) Get(name string) (*Command, bool) {
	if cmd, ok := r.commands[name]; ok {
		return cmd, true
	}
	if canonical, ok := r.aliases[name]; ok {
		return r.commands[canonical], true
	}
	return nil, false
}

// All returns commands grouped by category, sorted.
func (r *Registry) All() map[string][]*Command {
	groups := make(map[string][]*Command)
	for _, cmd := range r.commands {
		if cmd.Hidden {
			continue
		}
		groups[cmd.Category] = append(groups[cmd.Category], cmd)
	}
	for cat := range groups {
		sort.Slice(groups[cat], func(i, j int) bool {
			return groups[cat][i].Name < groups[cat][j].Name
		})
	}
	return groups
}

// Names returns all command names for autocomplete.
func (r *Registry) Names() []string {
	var names []string
	for name, cmd := range r.commands {
		if !cmd.Hidden {
			names = append(names, name)
		}
	}
	for alias, canonical := range r.aliases {
		if cmd := r.commands[canonical]; !cmd.Hidden {
			names = append(names, alias)
		}
	}
	sort.Strings(names)
	return names
}

// Parse takes raw user input like "/analyze . --json" and returns
// the matched command + parsed args.
func (r *Registry) Parse(input string) (*Command, ParsedArgs, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, ParsedArgs{}, fmt.Errorf("empty input")
	}

	// Strip leading / if present
	if input[0] == '/' {
		input = input[1:]
	}

	parts := tokenize(input)
	if len(parts) == 0 {
		return nil, ParsedArgs{}, fmt.Errorf("empty command")
	}

	cmd, ok := r.Get(parts[0])
	if !ok {
		// If there's a fallback, route the entire input to it
		if r.fallback != "" {
			if fb, exists := r.commands[r.fallback]; exists {
				return fb, ParsedArgs{
					Positional: []string{input},
					Flags:      make(map[string]string),
					Raw:        input,
				}, nil
			}
		}
		return nil, ParsedArgs{}, fmt.Errorf("unknown command: %s — type /help for commands, or just ask a question", parts[0])
	}

	args, err := parseArgs(parts[1:], cmd.Args)
	if err != nil {
		return cmd, args, fmt.Errorf("%s: %w", cmd.Name, err)
	}
	args.Raw = input

	return cmd, args, nil
}

func parseArgs(parts []string, spec []Arg) (ParsedArgs, error) {
	pa := ParsedArgs{Flags: make(map[string]string)}

	// Build lookup for flags
	flagSpec := make(map[string]*Arg)
	shortSpec := make(map[string]*Arg)
	for i := range spec {
		if spec[i].IsFlag || !spec[i].Required {
			flagSpec["--"+spec[i].Name] = &spec[i]
			if spec[i].Short != "" {
				shortSpec["-"+spec[i].Short] = &spec[i]
			}
		}
	}

	// Set defaults
	for _, a := range spec {
		if a.Default != "" {
			pa.Flags[a.Name] = a.Default
		}
	}

	for i := 0; i < len(parts); i++ {
		p := parts[i]

		// --key=value
		if strings.HasPrefix(p, "--") && strings.Contains(p, "=") {
			kv := strings.SplitN(p[2:], "=", 2)
			pa.Flags[kv[0]] = kv[1]
			continue
		}

		// --flag or --key value
		if a, ok := flagSpec[p]; ok {
			if a.IsFlag {
				pa.Flags[a.Name] = "true"
			} else if i+1 < len(parts) {
				i++
				pa.Flags[a.Name] = parts[i]
			}
			continue
		}

		// -f short flag
		if a, ok := shortSpec[p]; ok {
			if a.IsFlag {
				pa.Flags[a.Name] = "true"
			} else if i+1 < len(parts) {
				i++
				pa.Flags[a.Name] = parts[i]
			}
			continue
		}

		// Positional
		pa.Positional = append(pa.Positional, p)
	}

	// Check required positional args
	requiredPos := 0
	for _, a := range spec {
		if a.Required && !a.IsFlag {
			requiredPos++
		}
	}
	if len(pa.Positional) < requiredPos {
		return pa, fmt.Errorf("expected %d argument(s), got %d", requiredPos, len(pa.Positional))
	}

	return pa, nil
}

// tokenize splits input respecting quoted strings.
func tokenize(input string) []string {
	var tokens []string
	var current strings.Builder
	inQuote := false
	quoteChar := rune(0)

	for _, ch := range input {
		switch {
		case !inQuote && (ch == '"' || ch == '\''):
			inQuote = true
			quoteChar = ch
		case inQuote && ch == quoteChar:
			inQuote = false
		case !inQuote && ch == ' ':
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(ch)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}
