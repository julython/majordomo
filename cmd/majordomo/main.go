package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/julython/majordomo/internal/commands"
	"github.com/julython/majordomo/internal/config"
	"github.com/julython/majordomo/internal/jobs"
	"github.com/julython/majordomo/internal/knowledge"
	"github.com/julython/majordomo/internal/llm"
	"github.com/julython/majordomo/internal/tui"
)

func main() {
	repoDir := "."
	tracker := jobs.NewTracker()

	// Load config (falls back to defaults if no file)
	cfg, err := config.Load("")
	if err != nil {
		slog.Info("no config found, using defaults", "error", err)
		cfg = config.Default()
	}

	kb, err := knowledge.Open(repoDir)
	if err != nil {
		slog.Warn("knowledge store", "error", err)
		kb = &knowledge.Store{}
	}

	// Create LLM client from config
	client := llm.FromConfig(cfg)

	deps := &commands.Deps{
		Tracker: tracker,
		KB:      kb,
		LLM:     client,
		RepoDir: repoDir,
	}

	reg := commands.NewRegistry()
	commands.RegisterAll(reg, deps)

	// No args → interactive chat
	if len(os.Args) < 2 {
		runInteractive(reg)
		return
	}

	// Direct CLI
	input := strings.Join(os.Args[1:], " ")
	cmd, args, err := reg.Parse(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	sink := tui.StdoutSink()
	if err := cmd.Run(context.Background(), args, sink); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runInteractive(reg *commands.Registry) {
	c := tui.NewChat(reg)
	chat := &c
	p := tea.NewProgram(
		chat,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	chat.SetProgram(p)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
