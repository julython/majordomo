package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/julython/majordomo/internal/analyze"
	"github.com/julython/majordomo/internal/config"
	"github.com/julython/majordomo/internal/grade"
	"github.com/julython/majordomo/internal/llm"
	"github.com/julython/majordomo/internal/worker"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	cfg, err := config.Load("")
	if err != nil {
		slog.Warn("using default config", "error", err)
		cfg = config.Default()
	}

	var exitErr error
	switch os.Args[1] {
	case "analyze":
		exitErr = cmdAnalyze(ctx, cfg, os.Args[2:])
	case "grade":
		exitErr = cmdGrade(ctx, cfg, os.Args[2:])
	case "watch":
		exitErr = cmdWatch(ctx, cfg, os.Args[2:])
	case "setup":
		exitErr = cmdSetup(ctx, cfg)
	case "login":
		exitErr = cmdLogin(ctx, cfg, os.Args[2:])
	case "status":
		exitErr = cmdStatus(cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}

	if exitErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", exitErr)
		os.Exit(1)
	}
}

func cmdAnalyze(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("analyze", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "output raw JSON")
	noLLM := fs.Bool("no-llm", false, "skip LLM narrative")
	model := fs.String("model", "", "override model name")
	fs.Parse(args)

	path := "."
	if fs.NArg() > 0 {
		path = fs.Arg(0)
	}
	path, _ = filepath.Abs(path)

	var client llm.Client
	if !*noLLM {
		var err error
		client, err = llm.New(cfg, *model)
		if err != nil {
			slog.Warn("no LLM available, running stats-only", "error", err)
		}
	}

	return analyze.Run(ctx, path, client, *jsonOut)
}

func cmdGrade(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("grade", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "output raw scorecard JSON")
	fs.Parse(args)

	path := "."
	if fs.NArg() > 0 {
		path = fs.Arg(0)
	}
	path, _ = filepath.Abs(path)

	data, err := analyze.Collect(ctx, path)
	if err != nil {
		return err
	}
	report := grade.FromData(data.ToGradeInput())

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	grade.PrintReport(report)
	return nil
}

func cmdWatch(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	interval := fs.Int("interval", 5, "poll interval in seconds")
	fs.Parse(args)

	path := "."
	if fs.NArg() > 0 {
		path = fs.Arg(0)
	}
	path, _ = filepath.Abs(path)

	client, err := llm.New(cfg, "")
	if err != nil {
		slog.Warn("no LLM, worker will submit raw stats only", "error", err)
	}

	return worker.Run(ctx, cfg, path, client, *interval)
}

func cmdSetup(ctx context.Context, cfg *config.Config) error {
	return config.InteractiveSetup(ctx, cfg)
}

func cmdLogin(ctx context.Context, cfg *config.Config, args []string) error {
	server := cfg.Server.URL
	if len(args) > 0 {
		server = args[0]
	}
	return config.DeviceFlowLogin(ctx, server)
}

func cmdStatus(cfg *config.Config) error {
	fmt.Printf("Server: %s\n", cfg.Server.URL)
	fmt.Printf("LLM:    %s (%s)\n", cfg.LLM.Provider, cfg.LLM.Model)
	if _, err := cfg.Token(); err != nil {
		fmt.Println("Auth:   not logged in")
	} else {
		fmt.Println("Auth:   logged in")
	}
	return nil
}

func printUsage() {
	fmt.Println(`majordomo — AI-powered repo health and PR triage

Usage: majordomo <command> [options]

Commands:
  analyze [path]   Analyze and grade a repository
  grade [path]     Output scorecard only
  watch [path]     Poll server for jobs
  setup            Interactive first-run setup
  login [server]   Authenticate with server
  status           Show current config`)
}
