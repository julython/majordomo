package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	ctx "github.com/julython/repomap/context"
	"github.com/julython/repomap/graph"
	"github.com/julython/repomap/indexer"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	switch cmd {
	case "index":
		cmdIndex()
	case "symbols":
		cmdSymbols()
	case "context":
		cmdContext()
	case "prompt":
		cmdPrompt()
	case "refs":
		cmdRefs()
	case "files":
		cmdFiles()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `repomap — fast static knowledge graph for code repositories

Usage:
  repomap index   [path]                       Index a repo and print summary
  repomap symbols [path] [query]               Search symbols by name
  repomap context [path] [symbol]              Show assembled context (human-readable)
  repomap prompt  [path] [symbol] [task]       Show the LLM-ready prompt for an edit
  repomap refs    [path] [symbol]              Show who calls a symbol and what it calls
  repomap files   [path]                       List indexed files with symbol counts

Options:
  REPOMAP_BUDGET=N  Set token budget (default 4096). Example: REPOMAP_BUDGET=8192 repomap prompt ...

If path is omitted, uses current directory.`)
}

// repoRoot finds the repo root from a starting path (walks up to find .git).
func repoRoot(start string) string {
	abs, _ := filepath.Abs(start)
	for {
		if _, err := os.Stat(filepath.Join(abs, ".git")); err == nil {
			return abs
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			// Hit filesystem root — just use the starting path
			a, _ := filepath.Abs(start)
			return a
		}
		abs = parent
	}
}

func getPath() string {
	if len(os.Args) >= 3 {
		return os.Args[2]
	}
	dir, _ := os.Getwd()
	return dir
}

var lastRefStats indexer.RefStats

func buildGraph(root string) *indexer.Indexer {
	idx := indexer.New(root)
	// Note: caller should defer idx.Close() — but for a CLI that exits
	// immediately, the OS reclaims the memory anyway. For long-lived
	// processes, always defer Close().
	if err := idx.Index(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	idx.ResolveImportEdges()
	lastRefStats = idx.ResolveReferences()
	return idx
}

// --- Commands ---

func cmdIndex() {
	root := repoRoot(getPath())
	idx := buildGraph(root)

	stats := idx.Graph.Stats()
	fmt.Printf("Indexed %s in %s\n", root, idx.IndexDuration)
	fmt.Printf("  Files:   %d scanned, %d skipped, %d parse errors\n",
		idx.FilesScanned, idx.FilesSkipped, idx.ParseErrors)
	fmt.Printf("  Symbols: %d total\n", stats.Symbols)
	fmt.Printf("  Edges:   %d total\n", stats.Edges)
	fmt.Println()

	// Language breakdown
	fmt.Println("  Languages:")
	for lang, count := range stats.ByLanguage {
		fmt.Printf("    %-12s %d files\n", lang, count)
	}

	// Kind breakdown
	fmt.Println("  Symbol kinds:")
	for kind, count := range stats.ByKind {
		fmt.Printf("    %-12s %d\n", kind, count)
	}

	// Reference stats
	fmt.Println("  References:")
	fmt.Printf("    found=%d resolved=%d unresolved=%d (%.0f%% hit rate)\n",
		lastRefStats.RefsFound, lastRefStats.RefsResolved, lastRefStats.RefsUnresolved,
		func() float64 {
			if lastRefStats.RefsFound == 0 {
				return 0
			}
			return float64(lastRefStats.RefsResolved) / float64(lastRefStats.RefsFound) * 100
		}())
}

func cmdSymbols() {
	root := repoRoot(getPath())

	query := ""
	if len(os.Args) >= 4 {
		query = os.Args[3]
	} else if len(os.Args) >= 3 {
		// Could be a query if it doesn't look like a path
		candidate := os.Args[2]
		if info, err := os.Stat(candidate); err != nil || !info.IsDir() {
			query = candidate
			root = repoRoot(".")
		}
	}

	idx := buildGraph(root)

	var results []*graph.Symbol
	if query == "" {
		// Dump all symbols
		for _, sym := range idx.Graph.Symbols {
			results = append(results, sym)
		}
	} else {
		// Exact match first
		results = idx.Graph.LookupName(query)
		if len(results) == 0 {
			// Fuzzy fallback
			results = idx.Graph.FuzzyLookup(query)
		}
	}

	// Sort by file then line
	sort.Slice(results, func(i, j int) bool {
		if results[i].File != results[j].File {
			return results[i].File < results[j].File
		}
		return results[i].StartLine < results[j].StartLine
	})

	for _, sym := range results {
		exported := " "
		if sym.Exported {
			exported = "+"
		}
		fmt.Printf("%s %-10s %-40s %s:%d-%d\n",
			exported, sym.Kind, sym.Name, sym.File, sym.StartLine, sym.EndLine)
	}

	if len(results) == 0 && query != "" {
		fmt.Fprintf(os.Stderr, "no symbols matching %q\n", query)
		os.Exit(1)
	}
}

func cmdContext() {
	root, symbolQuery := parseSymbolArgs("context")
	idx := buildGraph(root)
	target := resolveTarget(idx, symbolQuery)

	asm := ctx.NewAssembler(idx.Graph, root)
	asm.Budget = getBudget()

	assembled, err := asm.ForSymbol(target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(ctx.RenderHuman(assembled))
}

func cmdPrompt() {
	root, symbolQuery := parseSymbolArgs("prompt")

	// Task is the remaining args after symbol
	task := ""
	if len(os.Args) >= 5 {
		task = strings.Join(os.Args[4:], " ")
	} else if len(os.Args) >= 4 {
		// Might be: prompt <symbol> <task> (no path)
		candidate := os.Args[2]
		if info, err := os.Stat(candidate); err != nil || !info.IsDir() {
			task = strings.Join(os.Args[3:], " ")
		} else {
			task = os.Args[3]
		}
	}

	idx := buildGraph(root)
	target := resolveTarget(idx, symbolQuery)

	asm := ctx.NewAssembler(idx.Graph, root)
	asm.Budget = getBudget()

	assembled, err := asm.ForSymbol(target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	opts := ctx.PromptOptions{
		Operation: ctx.OpReplace,
		Task:      task,
	}

	fmt.Print(ctx.RenderPrompt(assembled, opts))
}

// --- Shared helpers for symbol commands ---

func parseSymbolArgs(cmd string) (root string, symbolQuery string) {
	if len(os.Args) >= 4 {
		root = repoRoot(os.Args[2])
		symbolQuery = os.Args[3]
	} else if len(os.Args) >= 3 {
		symbolQuery = os.Args[2]
		root = repoRoot(".")
	} else {
		fmt.Fprintf(os.Stderr, "usage: repomap %s [path] <symbol>\n", cmd)
		os.Exit(1)
	}
	return
}

func resolveTarget(idx *indexer.Indexer, query string) *graph.Symbol {
	targets := idx.Graph.LookupName(query)
	if len(targets) == 0 {
		targets = idx.Graph.FuzzyLookup(query)
	}
	if len(targets) == 0 {
		fmt.Fprintf(os.Stderr, "no symbol matching %q\n", query)
		os.Exit(1)
	}
	if len(targets) > 1 {
		fmt.Fprintf(os.Stderr, "# Found %d matches, using: %s\n", len(targets), targets[0].ID)
	}
	return targets[0]
}

func getBudget() ctx.Budget {
	budget := ctx.DefaultBudget
	if env := os.Getenv("REPOMAP_BUDGET"); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n > 0 {
			budget.MaxTokens = n
		}
	}
	return budget
}

func cmdRefs() {
	root := repoRoot(getPath())

	symbolQuery := ""
	if len(os.Args) >= 4 {
		symbolQuery = os.Args[3]
	} else if len(os.Args) >= 3 {
		symbolQuery = os.Args[2]
		root = repoRoot(".")
	} else {
		fmt.Fprintln(os.Stderr, "usage: repomap refs [path] <symbol>")
		os.Exit(1)
	}

	idx := buildGraph(root)

	targets := idx.Graph.LookupName(symbolQuery)
	if len(targets) == 0 {
		targets = idx.Graph.FuzzyLookup(symbolQuery)
	}
	if len(targets) == 0 {
		fmt.Fprintf(os.Stderr, "no symbol matching %q\n", symbolQuery)
		os.Exit(1)
	}

	for _, target := range targets {
		fmt.Printf("=== %s (%s:%d) ===\n\n", target.ID, target.File, target.StartLine)

		// Who calls this? (dependents)
		dependents := idx.Graph.Dependents(target.ID)
		if len(dependents) > 0 {
			fmt.Printf("  Called by (%d):\n", len(dependents))
			seen := make(map[graph.SymbolID]bool)
			for _, depID := range dependents {
				if seen[depID] {
					continue
				}
				seen[depID] = true
				if dep, ok := idx.Graph.Symbols[depID]; ok {
					fmt.Printf("    ← %s  %s:%d\n", dep.SignatureOrFallback(), dep.File, dep.StartLine)
				} else {
					// File-level reference
					fmt.Printf("    ← (file-level) %s\n", depID)
				}
			}
		} else {
			fmt.Println("  Called by: (none found)")
		}
		fmt.Println()

		// What does this call? (dependencies)
		deps := idx.Graph.Dependencies(target.ID)
		if len(deps) > 0 {
			fmt.Printf("  Calls (%d):\n", len(deps))
			seen := make(map[graph.SymbolID]bool)
			for _, depID := range deps {
				if seen[depID] {
					continue
				}
				seen[depID] = true
				if dep, ok := idx.Graph.Symbols[depID]; ok {
					fmt.Printf("    → %s  %s:%d\n", dep.SignatureOrFallback(), dep.File, dep.StartLine)
				}
			}
		} else {
			fmt.Println("  Calls: (none found)")
		}
		fmt.Println()
	}
}

func cmdFiles() {
	root := repoRoot(getPath())
	idx := buildGraph(root)

	type fileStat struct {
		path    string
		lang    graph.Language
		symbols int
		imports int
	}

	var files []fileStat
	for path, f := range idx.Graph.Files {
		files = append(files, fileStat{
			path:    path,
			lang:    f.Language,
			symbols: len(f.Symbols),
			imports: len(f.Imports),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].path < files[j].path
	})

	fmt.Printf("%-12s %6s %7s  %s\n", "LANGUAGE", "SYMS", "IMPORTS", "FILE")
	fmt.Println(strings.Repeat("-", 72))
	for _, f := range files {
		fmt.Printf("%-12s %6d %7d  %s\n", f.lang, f.symbols, f.imports, f.path)
	}
}
