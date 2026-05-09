package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/julython/majordomo/internal/analyze"
	"github.com/julython/majordomo/internal/config"
	"github.com/julython/majordomo/internal/jobs"
	"github.com/julython/majordomo/internal/knowledge"
	"github.com/julython/majordomo/internal/llm"
	"github.com/julython/majordomo/internal/repomap/ctx"
	"github.com/julython/majordomo/internal/repomap/graph"
	"github.com/julython/majordomo/internal/repomap/indexer"
	"github.com/julython/majordomo/internal/repomap/planner"
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
	r.Register(repomapCommands(deps))
	r.Register(chatWithToolsCommand(deps, r))
	r.Register(statusCommand(deps))
	r.Register(knowledgeCommand(deps))
	r.Register(resolveCommand(deps))
	r.Register(forgetCommand(deps))
	r.Register(watchCommand(deps))
	r.Register(cancelCommand(deps))
	r.Register(modelCommand(deps))
	r.Register(loginCommand(deps))
	r.Register(configCommand(deps))
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

func configCommand(deps *Deps) *Command {
	return &Command{
		Name:        "config",
		Aliases:     []string{"cfg", "settings"},
		Description: "Open interactive configuration editor",
		Usage:       "/config [--show] [--reset]",
		Category:    "config",
		Args: []Arg{
			{Name: "show", Short: "s", Description: "Show current config (no editor)", IsFlag: true},
			{Name: "reset", Short: "r", Description: "Reset to defaults", IsFlag: true},
		},
		Run: func(ctx context.Context, args ParsedArgs, sink Sink) error {
			cfg, err := config.Load("")
			if err != nil {
				cfg = config.Default()
			}

			// Reset to defaults
			if args.Flags["reset"] == "true" {
				cfg = config.Default()
				if err := config.Save(cfg); err != nil {
					sink.Error(fmt.Sprintf("Failed to save config: %v", err))
					return nil
				}
				sink.Print("✓ Config reset to defaults")
				sink.Print("")
				showConfig(sink, cfg)
				return nil
			}

			// Show only (non-interactive)
			if args.Flags["show"] == "true" {
				showConfig(sink, cfg)
				return nil
			}

			// Check if we're in TUI mode - if so, open the interactive editor
			if streamSink, ok := sink.(interface{ OpenConfig() }); ok {
				streamSink.OpenConfig()
				return nil
			}

			// CLI mode - just show the config
			sink.Print("Current configuration:")
			sink.Print("")
			showConfig(sink, cfg)
			sink.Print("")
			sink.Print("To edit: use --show, --reset, or edit ~/.config/majordomo/config.toml")
			return nil
		},
	}
}

func showConfig(sink Sink, cfg *config.Config) {
	sink.Print(fmt.Sprintf("  server.url      = %s", cfg.Server.URL))
	sink.Print(fmt.Sprintf("  llm.provider    = %s", cfg.LLM.Provider))
	sink.Print(fmt.Sprintf("  llm.model       = %s", cfg.LLM.Model))
	sink.Print(fmt.Sprintf("  llm.url         = %s", cfg.LLM.URL))
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

func repomapCommands(deps *Deps) *Command {
	return &Command{
		Name:        "repomap",
		Aliases:     []string{"rm", "symbols", "index", "graph"},
		Description: "Index the codebase and explore the symbol graph",
		Usage:       "/repomap <subcommand> [args]",
		Category:    "analysis",
		Args: []Arg{
			{Name: "subcommand", Description: "index|symbols|context|prompt|plan|exec|refs|files", Required: true},
			{Name: "path", Description: "Repo path", Default: "."},
			{Name: "symbol", Description: "Symbol name (for context/prompt/refs)"},
			{Name: "query", Description: "Search query (for symbols)"},
			{Name: "task", Description: "Task description (for prompt/plan)"},
			{Name: "plan_file", Description: "Plan JSON file (for exec)"},
			{Name: "budget", Description: "Token budget (default 4096)"},
		},
		Run: func(ctx context.Context, args ParsedArgs, sink Sink) error {
			path := deps.RepoDir
			if len(args.Positional) > 1 {
				p := args.Positional[1]
				if _, err := os.Stat(p); err == nil {
					path = p
				}
			}

			subCmd := ""
			if len(args.Positional) > 0 {
				subCmd = args.Positional[0]
			}

			switch subCmd {
			case "index", "i":
				cmdIndex(path, sink)
			case "symbols", "s":
				cmdSymbols(path, args, sink)
			case "context", "c":
				cmdContext(path, args, sink)
			case "prompt", "p":
				cmdPrompt(path, args, sink)
			case "plan", "pl":
				cmdPlan(path, args, sink)
			case "exec", "e":
				cmdExec(path, args, sink)
			case "refs", "r":
				cmdRefs(path, args, sink)
			case "files", "f":
				cmdFiles(path, sink)
			default:
				sink.Error(fmt.Sprintf("unknown subcommand: %s", subCmd))
				sink.Print("Usage: /repomap index|symbols|context|prompt|plan|exec|refs|files [path] [args]")
			}
			return nil
		},
	}
}

// --- repomap subcommand implementations ---

func repoRoot(start string) string {
	abs, _ := filepath.Abs(start)
	for {
		if _, err := os.Stat(filepath.Join(abs, ".git")); err == nil {
			return abs
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			a, _ := filepath.Abs(start)
			return a
		}
		abs = parent
	}
}

func resolveTarget(idx *indexer.Indexer, query string) *graph.Symbol {
	targets := idx.Graph.LookupName(query)
	if len(targets) == 0 {
		targets = idx.Graph.FuzzyLookup(query)
	}
	if len(targets) == 0 {
		return nil
	}
	return targets[0]
}

func cmdIndex(path string, sink Sink) {
	root := repoRoot(path)
	sink.Status("Indexing repository...")

	idx := indexer.New(root)
	if err := idx.Index(); err != nil {
		sink.Error(fmt.Sprintf("index: %v", err))
		return
	}
	idx.ResolveImportEdges()
	refStats := idx.ResolveReferences()

	stats := idx.Graph.Stats()
	sink.PrintStyled(fmt.Sprintf("Indexed %s in %s", root, idx.IndexDuration))
	sink.Print(fmt.Sprintf("  Files:   %d scanned, %d skipped, %d parse errors",
		idx.FilesScanned, idx.FilesSkipped, idx.ParseErrors))
	sink.Print(fmt.Sprintf("  Symbols: %d total", stats.Symbols))
	sink.Print(fmt.Sprintf("  Edges:   %d total", stats.Edges))
	sink.Print("")

	sink.PrintStyled("  Languages:")
	for lang, count := range stats.ByLanguage {
		sink.Print(fmt.Sprintf("    %-12s %d files", lang, count))
	}

	sink.PrintStyled("  Symbol kinds:")
	for kind, count := range stats.ByKind {
		sink.Print(fmt.Sprintf("    %-12s %d", kind, count))
	}

	sink.PrintStyled("  References:")
	hitRate := float64(0)
	if refStats.RefsFound > 0 {
		hitRate = float64(refStats.RefsResolved) / float64(refStats.RefsFound) * 100
	}
	sink.Print(fmt.Sprintf("    found=%d resolved=%d unresolved=%d (%.0f%% hit rate)",
		refStats.RefsFound, refStats.RefsResolved, refStats.RefsUnresolved, hitRate))

	sink.Finish("")
	idx.Close()
}

func cmdSymbols(path string, args ParsedArgs, sink Sink) {
	root := repoRoot(path)
	query := ""
	if len(args.Positional) > 1 {
		candidate := args.Positional[1]
		if info, err := os.Stat(candidate); err != nil || !info.IsDir() {
			query = candidate
		}
	}
	if len(args.Positional) > 2 {
		query = args.Positional[2]
	}

	sink.Status("Searching symbols...")

	idx := indexer.New(root)

	var results []*graph.Symbol
	if query == "" {
		for _, sym := range idx.Graph.Symbols {
			results = append(results, sym)
		}
	} else {
		results = idx.Graph.LookupName(query)
		if len(results) == 0 {
			results = idx.Graph.FuzzyLookup(query)
		}
	}

	sink.Print(fmt.Sprintf("%-10s %-40s %s:%d-%d", "KIND", "NAME", "FILE", "LINE", "END"))

	for _, sym := range results {
		exported := " "
		if sym.Exported {
			exported = "+"
		}
		sink.Print(fmt.Sprintf("%s %-10s %-40s %s:%d-%d",
			exported, sym.Kind, sym.Name, sym.File, sym.StartLine, sym.EndLine))
	}

	sink.Finish(fmt.Sprintf("Found %d symbol(s)", len(results)))
	idx.Close()
}

func cmdContext(path string, args ParsedArgs, sink Sink) {
	symbolQuery := ""
	if len(args.Positional) > 1 {
		symbolQuery = args.Positional[1]
	}
	if len(args.Positional) > 2 {
		symbolQuery = args.Positional[2]
	}

	target := resolveSymbol(path, symbolQuery, sink)
	if target == nil {
		return
	}

	root := repoRoot(path)
	idx := indexer.New(root)
	sink.Status("Assembling context...")

	asm := ctx.NewAssembler(idx.Graph, root)
	asm.Budget = getBudget(args)

	assembled, err := asm.ForSymbol(target)
	if err != nil {
		sink.Error(fmt.Sprintf("context: %v", err))
		idx.Close()
		return
	}

	sink.PrintMarkdown(ctx.RenderHuman(assembled))
	sink.Finish("")
	idx.Close()
}

func cmdPrompt(path string, args ParsedArgs, sink Sink) {
	symbolQuery := ""
	task := ""
	if len(args.Positional) > 1 {
		candidate := args.Positional[1]
		if info, err := os.Stat(candidate); err != nil || !info.IsDir() {
			symbolQuery = candidate
		} else {
			path = candidate
		}
	}
	if len(args.Positional) > 2 {
		if symbolQuery == "" {
			symbolQuery = args.Positional[2]
		} else {
			task = strings.Join(args.Positional[2:], " ")
		}
	}
	if len(args.Positional) > 3 {
		task = strings.Join(args.Positional[2:], " ")
	}

	target := resolveSymbol(path, symbolQuery, sink)
	if target == nil {
		return
	}

	root := repoRoot(path)
	idx := indexer.New(root)
	sink.Status("Generating prompt...")

	asm := ctx.NewAssembler(idx.Graph, root)
	asm.Budget = getBudget(args)

	assembled, err := asm.ForSymbol(target)
	if err != nil {
		sink.Error(fmt.Sprintf("prompt: %v", err))
		return
	}

	opts := ctx.PromptOptions{
		Operation: ctx.OpReplace,
		Task:      task,
	}

	sink.PrintMarkdown(ctx.RenderPrompt(assembled, opts))
	sink.Finish("")
	idx.Close()
}

func cmdPlan(path string, args ParsedArgs, sink Sink) {
	task := ""
	if len(args.Positional) > 1 {
		candidate := args.Positional[1]
		if info, err := os.Stat(candidate); err != nil || !info.IsDir() {
			task = candidate
		} else {
			path = candidate
		}
	}
	if len(args.Positional) > 2 {
		task = strings.Join(args.Positional[2:], " ")
	}
	if len(args.Positional) > 3 {
		task = strings.Join(args.Positional[2:], " ")
	}

	sink.Status("Building planning prompt...")

	idx := indexer.New(repoRoot(path))
	p := planner.NewPlanner(idx.Graph, repoRoot(path))
	p.Budget = getBudget(args)

	sink.PrintMarkdown(p.BuildPlanningPrompt(task))
	sink.Finish("")
	idx.Close()
}

func cmdExec(path string, args ParsedArgs, sink Sink) {
	planArg := ""
	if len(args.Positional) > 1 {
		planArg = args.Positional[1]
	}

	if planArg == "" {
		sink.Error("specify a plan JSON file")
		return
	}

	planData, err := os.ReadFile(planArg)
	if err != nil {
		sink.Error(fmt.Sprintf("read plan: %v", err))
		return
	}

	plan, err := planner.ParsePlan(string(planData))
	if err != nil {
		sink.Error(fmt.Sprintf("parse plan: %v", err))
		return
	}

	sink.PrintStyled(fmt.Sprintf("%s", plan))

	idx := indexer.New(repoRoot(path))
	p := planner.NewPlanner(idx.Graph, repoRoot(path))
	p.Budget = getBudget(args)

	for i, step := range plan.Steps {
		if step.Action == planner.ActionRun {
			sink.Print(fmt.Sprintf("## Step %d: Run\n$ %s\n", i+1, step.Command))
			continue
		}

		if step.Action == planner.ActionDelete {
			prompt, err := p.BuildStepPrompt(step)
			if err != nil {
				sink.Error(fmt.Sprintf("  error: %v", err))
				continue
			}
			sink.PrintStyled(fmt.Sprintf("## Step %d: Delete\n%s", i+1, prompt))
			continue
		}

		prompt, err := p.BuildStepPrompt(step)
		if err != nil {
			sink.Error(fmt.Sprintf("  error: %v (skipping)", err))
			continue
		}

		sink.PrintStyled(fmt.Sprintf("## Step %d: %s %s", i+1, step.Action, step.Target))
		sink.PrintMarkdown(prompt)
		sink.PrintStyled("---")
		sink.Print("")
	}

	sink.Finish("")
	idx.Close()
}

func cmdRefs(path string, args ParsedArgs, sink Sink) {
	symbolQuery := ""
	if len(args.Positional) > 1 {
		symbolQuery = args.Positional[1]
	}
	if len(args.Positional) > 2 {
		symbolQuery = args.Positional[2]
	}

	target := resolveSymbol(path, symbolQuery, sink)
	if target == nil {
		return
	}

	root := repoRoot(path)
	idx := indexer.New(root)
	sink.Status("Resolving references...")

	sink.Print(fmt.Sprintf("=== %s (%s:%d) ===\n", target.ID, target.File, target.StartLine))

	dependents := idx.Graph.Dependents(target.ID)
	if len(dependents) > 0 {
		sink.PrintStyled(fmt.Sprintf("  Called by (%d):", len(dependents)))
		seen := make(map[graph.SymbolID]bool)
		for _, depID := range dependents {
			if seen[depID] {
				continue
			}
			seen[depID] = true
			if dep, ok := idx.Graph.Symbols[depID]; ok {
				sink.Print(fmt.Sprintf("    ← %s  %s:%d", dep.SignatureOrFallback(), dep.File, dep.StartLine))
			} else {
				sink.Print(fmt.Sprintf("    ← (file-level) %s", depID))
			}
		}
	} else {
		sink.Print("  Called by: (none found)")
	}
	sink.Print("")

	deps := idx.Graph.Dependencies(target.ID)
	if len(deps) > 0 {
		sink.PrintStyled(fmt.Sprintf("  Calls (%d):", len(deps)))
		seen := make(map[graph.SymbolID]bool)
		for _, depID := range deps {
			if seen[depID] {
				continue
			}
			seen[depID] = true
			if dep, ok := idx.Graph.Symbols[depID]; ok {
				sink.Print(fmt.Sprintf("    → %s  %s:%d", dep.SignatureOrFallback(), dep.File, dep.StartLine))
			}
		}
	} else {
		sink.Print("  Calls: (none found)")
	}

	sink.Finish("")
	idx.Close()
}

func cmdFiles(path string, sink Sink) {
	root := repoRoot(path)
	sink.Status("Listing files...")

	idx := indexer.New(root)

	type fileStat struct {
		path    string
		lang    graph.Language
		symbols int
		imports int
	}

	var files []fileStat
	for path2, f := range idx.Graph.Files {
		files = append(files, fileStat{
			path:    path2,
			lang:    f.Language,
			symbols: len(f.Symbols),
			imports: len(f.Imports),
		})
	}

	sink.Print(fmt.Sprintf("%-12s %6s %7s  %s", "LANGUAGE", "SYMS", "IMPORTS", "FILE"))
	sink.Print(strings.Repeat("-", 72))
	for _, f := range files {
		sink.Print(fmt.Sprintf("%-12s %6d %7d  %s", f.lang, f.symbols, f.imports, f.path))
	}

	sink.Finish("")
	idx.Close()
}

func resolveSymbol(path, query string, sink Sink) *graph.Symbol {
	if query == "" {
		sink.Error("specify a symbol name")
		return nil
	}
	root := repoRoot(path)
	idx := indexer.New(root)
	targets := idx.Graph.LookupName(query)
	if len(targets) == 0 {
		targets = idx.Graph.FuzzyLookup(query)
	}
	if len(targets) == 0 {
		sink.Error(fmt.Sprintf("no symbol matching %q", query))
		idx.Close()
		return nil
	}
	return targets[0]
}

func getBudget(args ParsedArgs) ctx.Budget {
	budget := ctx.DefaultBudget
	if b, ok := args.Flags["budget"]; ok && b != "" {
		if n, err := strconv.Atoi(b); err == nil && n > 0 {
			budget.MaxTokens = n
		}
	}
	return budget
}
