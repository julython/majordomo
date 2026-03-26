package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/julython/majordomo/internal/analyze"
	"github.com/julython/majordomo/internal/jobs"
	"github.com/julython/majordomo/internal/knowledge"
	"github.com/julython/majordomo/internal/llm"
)

// Deps holds shared dependencies that commands can use.
type Deps struct {
	Tracker *jobs.Tracker
	KB      *knowledge.Store
	LLM     llm.Client // nil if no model available
	RepoDir string
}

// RegisterAll adds every built-in command to the registry.
func RegisterAll(r *Registry, deps *Deps) {
	r.Register(helpCommand(r))
	r.Register(setupCommand(deps))
	r.Register(analyzeCommand(deps))
	r.Register(chatCommand(deps))
	r.Register(statusCommand(deps))
	r.Register(knowledgeCommand(deps))
	r.Register(resolveCommand(deps))
	r.Register(forgetCommand(deps))
	r.Register(watchCommand(deps))
	r.Register(cancelCommand(deps))
	r.Register(modelCommand(deps))
	r.Register(loginCommand(deps))
	r.Register(clearCommand())
	r.Register(quitCommand())

	// Unknown input goes to the LLM chat
	r.SetFallback("chat")
}

func helpCommand(reg *Registry) *Command {
	return &Command{
		Name:        "help",
		Aliases:     []string{"h", "?"},
		Description: "Show available commands",
		Usage:       "/help [command]",
		Category:    "general",
		Args: []Arg{
			{Name: "command", Description: "Command to get help for"},
		},
		Run: func(ctx context.Context, args ParsedArgs, sink Sink) error {
			if len(args.Positional) > 0 {
				cmd, ok := reg.Get(args.Positional[0])
				if !ok {
					sink.Error(fmt.Sprintf("unknown command: %s", args.Positional[0]))
					return nil
				}
				sink.Print(fmt.Sprintf("  %s — %s", cmd.Name, cmd.Description))
				sink.Print(fmt.Sprintf("  usage: %s", cmd.Usage))
				if len(cmd.Aliases) > 0 {
					sink.Print(fmt.Sprintf("  aliases: %s", strings.Join(cmd.Aliases, ", ")))
				}
				for _, a := range cmd.Args {
					req := ""
					if a.Required {
						req = " (required)"
					}
					sink.Print(fmt.Sprintf("    --%s  %s%s", a.Name, a.Description, req))
				}
				return nil
			}

			groups := reg.All()
			order := []string{"analysis", "worker", "model", "config", "general"}
			for _, cat := range order {
				cmds, ok := groups[cat]
				if !ok {
					continue
				}
				sink.Print(fmt.Sprintf("  %s", strings.ToUpper(cat)))
				for _, cmd := range cmds {
					sink.Print(fmt.Sprintf("    %-14s %s", "/"+cmd.Name, cmd.Description))
				}
				sink.Print("")
			}
			return nil
		},
	}
}

func setupCommand(deps *Deps) *Command {
	return &Command{
		Name:        "setup",
		Aliases:     []string{"init"},
		Description: "Initialize majordomo for this repo",
		Usage:       "/setup [path]",
		Category:    "config",
		Args: []Arg{
			{Name: "path", Description: "Repo path", Default: "."},
		},
		Run: func(ctx context.Context, args ParsedArgs, sink Sink) error {
			path := deps.RepoDir
			if len(args.Positional) > 0 {
				path = args.Positional[0]
			}

			kb, err := knowledge.Open(path)
			if err != nil {
				sink.Error(fmt.Sprintf("knowledge store: %v", err))
				return nil
			}

			if len(kb.Entries) > 0 {
				_, open, resolved := kb.Stats()
				sink.Print(fmt.Sprintf("📚 Already initialized — %d entries (%d open, %d resolved)", len(kb.Entries), open, resolved))
				sink.Print("   Run /analyze to refresh.")
				return nil
			}

			sink.Print("📁 Created .majordomo/")
			sink.Status("Running initial scan...")

			data, err := analyze.Collect(ctx, path)
			if err != nil {
				sink.Error(fmt.Sprintf("scan: %v", err))
				return nil
			}

			// Seed the knowledge base from the scan data
			input := data.ToGradeInput()
			observations := []struct {
				topic   string
				summary string
				present bool
			}{
				{"ci", "CI configuration", input.HasCI},
				{"ci", "Linter configured", input.HasLinter},
				{"ci", "Formatter configured", input.HasFormatter},
				{"ci", "Pre-commit hooks", input.HasPreCommit},
				{"ci", "Tests run in CI", input.TestsInCI},
				{"deps", "Lockfile present", input.HasLockfile},
				{"deps", "Security scanning", input.HasSecurityScan},
				{"docs", "Contributing guide", input.HasContributing},
				{"docs", "PR template", input.HasPRTemplate},
				{"docs", "Issue templates", input.HasIssueTemplates},
				{"docs", "CODEOWNERS", input.HasCodeowners},
				{"docs", "Architecture doc", input.HasArchDoc},
				{"docs", "API spec", input.HasAPISpec},
				{"docs", "ADRs/RFCs", input.HasADRs},
				{"docs", "Changelog", input.HasChangelog},
				{"docs", "Setup instructions in README", input.HasSetupInstructions},
				{"docs", "AI context files", input.HasAIContext},
				{"tests", "Integration tests", input.HasIntegrationTests},
			}

			for _, obs := range observations {
				summary := obs.summary
				if !obs.present {
					summary = fmt.Sprintf("Missing: %s", obs.summary)
				}
				kb.Add(knowledge.Entry{
					Kind:    "observation",
					Topic:   obs.topic,
					Summary: summary,
					Source:  "scan",
				})
			}

			_ = kb.Save()

			sink.Print(fmt.Sprintf("📁 %d files, %d source, %d tests",
				len(data.Files), len(data.SourceFiles), len(data.TestFiles)))
			sink.Print(fmt.Sprintf("📋 %d observations recorded", len(kb.Entries)))
			sink.Print("")
			sink.Print("Run /analyze for the full report card, or /kb to see observations.")
			sink.Finish("")
			return nil
		},
	}
}

func analyzeCommand(deps *Deps) *Command {
	return &Command{
		Name:        "analyze",
		Aliases:     []string{"a", "grade"},
		Description: "Scan the repo, grade it, and suggest improvements",
		Usage:       "/analyze [path] [--json] [--no-llm]",
		Category:    "analysis",
		Args: []Arg{
			{Name: "path", Description: "Repo path", Default: "."},
			{Name: "json", Short: "j", Description: "Output JSON", IsFlag: true},
			{Name: "no-llm", Description: "Skip LLM narrative", IsFlag: true},
		},
		Run: func(ctx context.Context, args ParsedArgs, sink Sink) error {
			path := deps.RepoDir
			if len(args.Positional) > 0 {
				path = args.Positional[0]
			}

			jsonOut := args.Flags["json"] == "true"

			var client llm.Client
			if args.Flags["no-llm"] != "true" {
				client = deps.LLM
			}

			// Wrap our command Sink as an analyze.Sink
			as := &analyzeSinkAdapter{sink}
			if err := analyze.RunWithSink(ctx, path, client, jsonOut, as); err != nil {
				sink.Error(fmt.Sprintf("analyze: %v", err))
			}
			return nil
		},
	}
}

// analyzeSinkAdapter bridges commands.Sink to analyze.Sink.
type analyzeSinkAdapter struct{ inner Sink }

func (a *analyzeSinkAdapter) Print(text string)         { a.inner.Print(text) }
func (a *analyzeSinkAdapter) PrintMarkdown(text string) { a.inner.PrintMarkdown(text) }
func (a *analyzeSinkAdapter) PrintStyled(line string)   { a.inner.PrintStyled(line) }
func (a *analyzeSinkAdapter) Status(text string)        { a.inner.Status(text) }
func (a *analyzeSinkAdapter) Error(text string)         { a.inner.Error(text) }
func (a *analyzeSinkAdapter) Finish(summary string)     { a.inner.Finish(summary) }

func chatCommand(deps *Deps) *Command {
	return &Command{
		Name:        "chat",
		Aliases:     []string{"ask"},
		Description: "Ask the LLM about this repo",
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

			prompt := buildChatPrompt(message, kb)

			sink.Status(fmt.Sprintf("Thinking (%s)...", deps.LLM.Name()))

			// Stream tokens — flush each line as it completes
			var lineBuf strings.Builder
			_, err = deps.LLM.Stream(ctx, prompt, func(token string) {
				for _, ch := range token {
					if ch == '\n' {
						sink.PrintMarkdown(lineBuf.String())
						lineBuf.Reset()
					} else {
						lineBuf.WriteRune(ch)
					}
				}
			})

			// Flush any remaining partial line
			if lineBuf.Len() > 0 {
				sink.PrintMarkdown(lineBuf.String())
			}

			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				sink.Error(fmt.Sprintf("LLM: %v", err))
			}

			return nil
		},
	}
}

func buildChatPrompt(message string, kb *knowledge.Store) string {
	var b strings.Builder

	b.WriteString(`You are majordomo, an AI assistant that helps developers understand and improve their projects. You are running locally on the user's machine, inside their repository.

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
		b.WriteString("### This repo has not been analyzed yet. Suggest running /analyze if relevant.\n\n")
	}

	b.WriteString("### User's message:\n")
	b.WriteString(message)
	b.WriteString("\n")

	return b.String()
}

func statusCommand(deps *Deps) *Command {
	return &Command{
		Name:        "status",
		Aliases:     []string{"s"},
		Description: "Show running jobs",
		Usage:       "/status",
		Category:    "worker",
		Run: func(ctx context.Context, args ParsedArgs, sink Sink) error {
			running := deps.Tracker.Running()
			if len(running) == 0 {
				sink.Print("No jobs running")
			} else {
				for _, j := range running {
					sink.Print(fmt.Sprintf("  ● %s (%s)", j.ID, j.Kind))
				}
			}
			return nil
		},
	}
}

func knowledgeCommand(deps *Deps) *Command {
	return &Command{
		Name:        "knowledge",
		Aliases:     []string{"k", "kb"},
		Description: "Show what majordomo knows about this repo",
		Usage:       "/knowledge [topic] [--open]",
		Category:    "analysis",
		Args: []Arg{
			{Name: "topic", Description: "Filter by topic"},
			{Name: "open", Short: "o", Description: "Show only open suggestions", IsFlag: true},
		},
		Run: func(ctx context.Context, args ParsedArgs, sink Sink) error {
			kb, err := knowledge.Open(deps.RepoDir)
			if err != nil {
				sink.Error(fmt.Sprintf("knowledge store: %v", err))
				return nil
			}

			if len(kb.Entries) == 0 {
				sink.Print("No knowledge yet. Run /analyze to start learning.")
				return nil
			}

			topic := ""
			if len(args.Positional) > 0 {
				topic = args.Positional[0]
			}
			onlyOpen := args.Flags["open"] == "true"

			for _, e := range kb.Entries {
				if topic != "" && e.Topic != topic {
					continue
				}
				if onlyOpen && (e.Kind != "suggestion" || e.Resolved) {
					continue
				}

				icon := "•"
				switch e.Kind {
				case "suggestion":
					icon = "💡"
				case "resolved":
					icon = "✅"
				case "observation":
					icon = "📋"
				case "note":
					icon = "📝"
				}

				sink.Print(fmt.Sprintf("  %s [%s] %s", icon, e.Topic, e.Summary))
				if e.Details != "" {
					sink.Print(fmt.Sprintf("      %s", e.Details))
				}
				sink.Print(fmt.Sprintf("      id: %s  source: %s", e.ID, e.Source))
			}
			return nil
		},
	}
}

func resolveCommand(deps *Deps) *Command {
	return &Command{
		Name:        "resolve",
		Aliases:     []string{"done", "fix"},
		Description: "Mark a suggestion as resolved",
		Usage:       "/resolve <id>",
		Category:    "analysis",
		Args: []Arg{
			{Name: "id", Description: "Entry ID to resolve", Required: true},
		},
		Run: func(ctx context.Context, args ParsedArgs, sink Sink) error {
			if len(args.Positional) == 0 {
				sink.Error("Specify an entry ID. Use /kb --open to see suggestions.")
				return nil
			}

			kb, err := knowledge.Open(deps.RepoDir)
			if err != nil {
				sink.Error(fmt.Sprintf("knowledge store: %v", err))
				return nil
			}

			id := args.Positional[0]
			if kb.Resolve(id) {
				_ = kb.Save()
				sink.Print(fmt.Sprintf("✅ Resolved %s", id))
			} else {
				sink.Error(fmt.Sprintf("Entry not found: %s", id))
			}
			return nil
		},
	}
}

func forgetCommand(deps *Deps) *Command {
	return &Command{
		Name:        "forget",
		Description: "Remove a knowledge entry",
		Usage:       "/forget <id | --all>",
		Category:    "analysis",
		Args: []Arg{
			{Name: "all", Short: "a", Description: "Forget everything", IsFlag: true},
		},
		Run: func(ctx context.Context, args ParsedArgs, sink Sink) error {
			kb, err := knowledge.Open(deps.RepoDir)
			if err != nil {
				sink.Error(fmt.Sprintf("knowledge store: %v", err))
				return nil
			}

			if args.Flags["all"] == "true" {
				count := len(kb.Entries)
				kb.Entries = nil
				kb.LastReport = nil
				_ = kb.Save()
				sink.Print(fmt.Sprintf("🗑  Forgot %d entries", count))
				return nil
			}

			if len(args.Positional) == 0 {
				sink.Error("Specify an entry ID or --all")
				return nil
			}

			id := args.Positional[0]
			if kb.Remove(id) {
				_ = kb.Save()
				sink.Print(fmt.Sprintf("🗑  Forgot %s", id))
			} else {
				sink.Error(fmt.Sprintf("Entry not found: %s", id))
			}
			return nil
		},
	}
}

func watchCommand(deps *Deps) *Command {
	return &Command{
		Name:        "watch",
		Aliases:     []string{"w"},
		Description: "Connect as a worker and poll for jobs",
		Usage:       "/watch [path]",
		Category:    "worker",
		Args: []Arg{
			{Name: "path", Description: "Repo path", Default: "."},
		},
		Run: func(ctx context.Context, args ParsedArgs, sink Sink) error {
			sink.Status("Connecting to server...")
			// TODO: register worker, start poll loop
			sink.Print("watch: not yet implemented")
			sink.Finish("")
			return nil
		},
	}
}

func cancelCommand(deps *Deps) *Command {
	return &Command{
		Name:        "cancel",
		Aliases:     []string{"x"},
		Description: "Cancel running jobs",
		Usage:       "/cancel [job-id | --all]",
		Category:    "worker",
		Args: []Arg{
			{Name: "all", Short: "a", Description: "Cancel all", IsFlag: true},
		},
		Run: func(ctx context.Context, args ParsedArgs, sink Sink) error {
			if args.Flags["all"] == "true" {
				n := deps.Tracker.CancelAll()
				sink.Print(fmt.Sprintf("⊘ Cancelled %d job(s)", n))
				return nil
			}

			if len(args.Positional) > 0 {
				if err := deps.Tracker.Cancel(args.Positional[0]); err != nil {
					sink.Error(err.Error())
				} else {
					sink.Print(fmt.Sprintf("⊘ Cancelled %s", args.Positional[0]))
				}
				return nil
			}

			running := deps.Tracker.Running()
			if len(running) == 0 {
				sink.Print("Nothing running")
				return nil
			}
			for _, j := range running {
				_ = deps.Tracker.Cancel(j.ID)
				sink.Print(fmt.Sprintf("⊘ Cancelled %s (%s)", j.ID, j.Kind))
			}
			return nil
		},
	}
}

func modelCommand(deps *Deps) *Command {
	return &Command{
		Name:        "model",
		Aliases:     []string{"m"},
		Description: "Manage local LLM models",
		Usage:       "/model [download|list|use]",
		Category:    "model",
		Run: func(ctx context.Context, args ParsedArgs, sink Sink) error {
			sink.Print("model: not yet implemented")
			return nil
		},
	}
}

func loginCommand(deps *Deps) *Command {
	return &Command{
		Name:        "login",
		Description: "Authenticate with the Julython server",
		Usage:       "/login",
		Category:    "config",
		Run: func(ctx context.Context, args ParsedArgs, sink Sink) error {
			sink.Print("login: not yet implemented")
			return nil
		},
	}
}

func quitCommand() *Command {
	return &Command{
		Name:        "quit",
		Aliases:     []string{"exit", "q"},
		Description: "Exit majordomo",
		Usage:       "/quit",
		Category:    "general",
		// Handled directly by the TUI before dispatch
		Run: func(ctx context.Context, args ParsedArgs, sink Sink) error { return nil },
	}
}

func clearCommand() *Command {
	return &Command{
		Name:        "clear",
		Aliases:     []string{"cls"},
		Description: "Clear the chat",
		Usage:       "/clear",
		Category:    "general",
		Hidden:      true,
		Run:         func(ctx context.Context, args ParsedArgs, sink Sink) error { return nil },
	}
}
