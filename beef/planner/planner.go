package planner

import (
	"fmt"
	"sort"
	"strings"

	appctx "github.com/julython/repomap/context"
	"github.com/julython/repomap/graph"
)

// Planner uses the knowledge graph to produce LLM prompts for planning
// and execution. It never calls the LLM itself — the caller handles that.
type Planner struct {
	Graph  *graph.Graph
	Root   string
	Budget appctx.Budget
}

// NewPlanner creates a planner from an indexed graph.
func NewPlanner(g *graph.Graph, root string) *Planner {
	return &Planner{
		Graph:  g,
		Root:   root,
		Budget: appctx.DefaultBudget,
	}
}

// --- Phase 1: Planning ---

// BuildPlanningPrompt produces the prompt that asks the LLM to create a plan.
// It includes the repo skeleton (file tree + symbol signatures) and the
// available operations. The caller sends this to the LLM and feeds the
// response to ParsePlan().
func (p *Planner) BuildPlanningPrompt(task string) string {
	var b strings.Builder

	b.WriteString("You are a senior engineer planning changes to a codebase. ")
	b.WriteString("Analyze the repository structure below and create a step-by-step plan.\n\n")

	b.WriteString("## Available operations\n")
	b.WriteString("- modify: edit an existing function/class/type\n")
	b.WriteString("- create: create a new file\n")
	b.WriteString("- add: add a new function/class/type to an existing file\n")
	b.WriteString("- delete: remove a function/class/type\n")
	b.WriteString("- run: execute a shell command (tests, build)\n\n")

	b.WriteString("## Repository structure\n")
	b.WriteString(p.buildSkeleton())
	b.WriteString("\n")

	b.WriteString("## Task\n")
	b.WriteString(task)
	b.WriteString("\n\n")

	b.WriteString("## Response format\n")
	b.WriteString("Respond with a JSON object:\n")
	b.WriteString("```json\n")
	b.WriteString("{\n")
	b.WriteString("  \"summary\": \"one-line description of the approach\",\n")
	b.WriteString("  \"steps\": [\n")
	b.WriteString("    {\"action\": \"modify\", \"target\": \"SymbolName\", \"file\": \"path/to/file.go\", \"task\": \"what to change\"},\n")
	b.WriteString("    {\"action\": \"create\", \"file\": \"path/to/new.go\", \"task\": \"what this file should contain\"},\n")
	b.WriteString("    {\"action\": \"add\", \"target\": \"NewFuncName\", \"file\": \"path/to/file.go\", \"after\": \"ExistingFunc\", \"task\": \"what the new function does\"},\n")
	b.WriteString("    {\"action\": \"run\", \"command\": \"go test ./...\", \"task\": \"verify changes\"}\n")
	b.WriteString("  ]\n")
	b.WriteString("}\n")
	b.WriteString("```\n")
	b.WriteString("Use only symbols and files that exist in the repository structure above, ")
	b.WriteString("unless the action is 'create' or 'add'. Be specific about what each step should accomplish.\n")

	return b.String()
}

// buildSkeleton produces a compact representation of the repo:
// file paths grouped by package/directory, with exported symbol signatures.
func (p *Planner) buildSkeleton() string {
	var b strings.Builder

	// Group files by directory
	type fileEntry struct {
		path    string
		node    *graph.FileNode
		symbols []*graph.Symbol
	}

	dirs := make(map[string][]fileEntry)
	for path, node := range p.Graph.Files {
		dir := dirOf(path)
		syms := p.Graph.SymbolsInFile(path)
		dirs[dir] = append(dirs[dir], fileEntry{path: path, node: node, symbols: syms})
	}

	// Sort directory names for deterministic output
	dirNames := make([]string, 0, len(dirs))
	for d := range dirs {
		dirNames = append(dirNames, d)
	}
	sort.Strings(dirNames)

	for _, dir := range dirNames {
		files := dirs[dir]
		sort.Slice(files, func(i, j int) bool {
			return files[i].path < files[j].path
		})

		if dir != "." {
			fmt.Fprintf(&b, "\n### %s/\n", dir)
		}

		for _, f := range files {
			// File header with language and package
			lang := f.node.Language.String()
			pkg := ""
			if f.node.Package != "" {
				pkg = fmt.Sprintf(" (package %s)", f.node.Package)
			}
			fmt.Fprintf(&b, "\n%s [%s%s]\n", f.path, lang, pkg)

			// Symbol signatures — exported first, then private
			// Sort: exported before unexported, then by line number
			syms := make([]*graph.Symbol, len(f.symbols))
			copy(syms, f.symbols)
			sort.Slice(syms, func(i, j int) bool {
				if syms[i].Exported != syms[j].Exported {
					return syms[i].Exported // exported first
				}
				return syms[i].StartLine < syms[j].StartLine
			})

			for _, sym := range syms {
				marker := "  "
				if sym.Exported {
					marker = "  +"
				}
				sig := sym.SignatureOrFallback()
				// Truncate very long signatures
				if len(sig) > 120 {
					sig = sig[:117] + "..."
				}
				fmt.Fprintf(&b, "%s %s\n", marker, sig)
			}
		}
	}

	return b.String()
}

// --- Phase 2: Execution ---

// BuildStepPrompt produces the LLM prompt for a single plan step.
// For modify/add/delete, it uses the assembler to gather focused context.
// For create, it provides the task + repo conventions.
// For run, it returns "" (the caller executes the command directly).
func (p *Planner) BuildStepPrompt(step Step) (string, error) {
	switch step.Action {
	case ActionModify:
		return p.buildModifyPrompt(step)
	case ActionAdd:
		return p.buildAddPrompt(step)
	case ActionCreate:
		return p.buildCreatePrompt(step)
	case ActionDelete:
		return p.buildDeletePrompt(step)
	case ActionRun:
		// Run steps are executed directly, not sent to the LLM
		return "", nil
	default:
		return "", fmt.Errorf("unknown action: %s", step.Action)
	}
}

func (p *Planner) buildModifyPrompt(step Step) (string, error) {
	// Find the target symbol
	target := p.findSymbol(step.Target, step.File)
	if target == nil {
		return "", fmt.Errorf("symbol %q not found in %s", step.Target, step.File)
	}

	asm := appctx.NewAssembler(p.Graph, p.Root)
	asm.Budget = p.Budget

	assembled, err := asm.ForSymbol(target)
	if err != nil {
		return "", err
	}

	opts := appctx.PromptOptions{
		Operation: appctx.OpReplace,
		Task:      step.Task,
	}

	return appctx.RenderPrompt(assembled, opts), nil
}

func (p *Planner) buildAddPrompt(step Step) (string, error) {
	var b strings.Builder

	fence := p.langFence(step.File)

	fmt.Fprintf(&b, "Add a new symbol to %s. ", step.File)
	fmt.Fprintf(&b, "Respond with ONLY the new code in a ```%s``` block.\n\n", fence)

	b.WriteString("## Task\n")
	b.WriteString(step.Task)
	b.WriteString("\n\n")

	// If there's an "after" hint, show that symbol + its neighbors for style
	if step.After != "" {
		afterSym := p.findSymbol(step.After, step.File)
		if afterSym != nil {
			asm := appctx.NewAssembler(p.Graph, p.Root)
			asm.Budget = p.Budget
			assembled, err := asm.ForSymbol(afterSym)
			if err == nil {
				b.WriteString("## Insert after this symbol\n")
				fmt.Fprintf(&b, "```%s\n%s\n```\n\n", fence, assembled.Target.Body)
			}
		}
	}

	// Show file imports and sibling signatures for style context
	if fileNode, ok := p.Graph.Files[step.File]; ok {
		if len(fileNode.Imports) > 0 {
			b.WriteString("## Current imports\n")
			for _, imp := range fileNode.Imports {
				b.WriteString(imp.Path)
				b.WriteByte('\n')
			}
			b.WriteByte('\n')
		}
	}

	syms := p.Graph.SymbolsInFile(step.File)
	if len(syms) > 0 {
		b.WriteString("## Existing symbols in file (match this style)\n")
		for _, sym := range syms {
			fmt.Fprintf(&b, "%s\n", sym.SignatureOrFallback())
		}
		b.WriteByte('\n')
	}

	return b.String(), nil
}

func (p *Planner) buildCreatePrompt(step Step) (string, error) {
	var b strings.Builder

	fence := p.langFence(step.File)

	fmt.Fprintf(&b, "Create a new file: %s\n", step.File)
	fmt.Fprintf(&b, "Respond with ONLY the complete file content in a ```%s``` block.\n\n", fence)

	b.WriteString("## Task\n")
	b.WriteString(step.Task)
	b.WriteString("\n\n")

	// Provide style context: show a similar existing file as an example
	example := p.findSimilarFile(step.File)
	if example != "" {
		syms := p.Graph.SymbolsInFile(example)
		if len(syms) > 0 {
			fmt.Fprintf(&b, "## Style reference: %s\n", example)
			if fileNode, ok := p.Graph.Files[example]; ok {
				if fileNode.Package != "" {
					fmt.Fprintf(&b, "package %s\n\n", fileNode.Package)
				}
				for _, imp := range fileNode.Imports {
					fmt.Fprintf(&b, "import %s\n", imp.Path)
				}
				b.WriteByte('\n')
			}
			for _, sym := range syms {
				fmt.Fprintf(&b, "%s\n", sym.SignatureOrFallback())
			}
			b.WriteByte('\n')
		}
	}

	// Show what other files in the target directory look like (package name, imports)
	targetDir := dirOf(step.File)
	for path, node := range p.Graph.Files {
		if dirOf(path) == targetDir && path != step.File {
			fmt.Fprintf(&b, "## Neighbor: %s (package %s)\n", path, node.Package)
			break
		}
	}

	return b.String(), nil
}

func (p *Planner) buildDeletePrompt(step Step) (string, error) {
	// Delete doesn't need an LLM prompt — the engine removes the symbol
	// at its byte range. Return a confirmation string.
	target := p.findSymbol(step.Target, step.File)
	if target == nil {
		return "", fmt.Errorf("symbol %q not found in %s", step.Target, step.File)
	}
	return fmt.Sprintf("# DELETE %s from %s (lines %d–%d)\n# Reason: %s\n",
		target.Name, target.File, target.StartLine, target.EndLine, step.Task), nil
}

// --- Helpers ---

// findSymbol locates a symbol by name, optionally scoped to a file.
func (p *Planner) findSymbol(name, file string) *graph.Symbol {
	if name == "" {
		return nil
	}

	// If file is specified, look there first
	if file != "" {
		for _, sym := range p.Graph.SymbolsInFile(file) {
			if sym.Name == name {
				return sym
			}
		}
	}

	// Global lookup
	matches := p.Graph.LookupName(name)
	if len(matches) > 0 {
		// If file was specified, prefer matches in that file
		if file != "" {
			for _, m := range matches {
				if m.File == file {
					return m
				}
			}
		}
		return matches[0]
	}

	// Fuzzy fallback
	fuzzy := p.Graph.FuzzyLookup(name)
	if len(fuzzy) > 0 {
		return fuzzy[0]
	}

	return nil
}

// findSimilarFile finds an existing file in the same directory or
// with a similar name pattern to use as a style reference.
func (p *Planner) findSimilarFile(targetPath string) string {
	targetDir := dirOf(targetPath)

	// First: same directory
	for path := range p.Graph.Files {
		if dirOf(path) == targetDir {
			return path
		}
	}

	// Second: similar name pattern (e.g., handler.go → handler_test.go files)
	targetBase := baseName(targetPath)
	for path := range p.Graph.Files {
		if strings.Contains(baseName(path), targetBase) {
			return path
		}
	}

	// Just return any file in the same language
	return ""
}

func (p *Planner) langFence(file string) string {
	if strings.HasSuffix(file, ".go") {
		return "go"
	}
	if strings.HasSuffix(file, ".py") || strings.HasSuffix(file, ".pyi") {
		return "python"
	}
	if strings.HasSuffix(file, ".ts") || strings.HasSuffix(file, ".tsx") {
		return "typescript"
	}
	if strings.HasSuffix(file, ".js") || strings.HasSuffix(file, ".jsx") {
		return "javascript"
	}
	return ""
}

func dirOf(path string) string {
	idx := strings.LastIndexByte(path, '/')
	if idx == -1 {
		return "."
	}
	return path[:idx]
}

func baseName(path string) string {
	idx := strings.LastIndexByte(path, '/')
	if idx == -1 {
		return path
	}
	name := path[idx+1:]
	// Strip extension
	if dot := strings.LastIndexByte(name, '.'); dot != -1 {
		name = name[:dot]
	}
	return name
}
