package context

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/julython/repomap/graph"
)

// Budget controls how much context to assemble.
// Roughly 1 token ≈ 4 characters for code.
type Budget struct {
	MaxTokens int // total token budget for the assembled context
}

var DefaultBudget = Budget{MaxTokens: 4096}

// SymbolWithBody pairs a symbol with its extracted source text.
type SymbolWithBody struct {
	Symbol *graph.Symbol
	Body   string
}

// AssembledContext is the full curated context package for a single target
// symbol. Everything the LLM needs, nothing it doesn't.
type AssembledContext struct {
	// Target is the symbol being edited, with its full source body.
	Target SymbolWithBody

	// File-level context
	FilePath string
	Imports  []graph.Import
	Language graph.Language

	// Structural context from the call graph
	Dependencies []SymbolWithSig // what the target calls (signatures only)
	Dependents   []SymbolWithSig // who calls the target (signatures only)

	// Broader context
	Siblings []SymbolWithSig  // other symbols in the same file
	Examples []SymbolWithBody // similar patterns in the repo (full bodies)
	Tests    []SymbolWithBody // related test functions (full bodies)

	// Metadata
	TokenEstimate int
	Truncated     bool // true if budget was exceeded and sections were cut
}

// SymbolWithSig is a lightweight reference: just the signature and location.
type SymbolWithSig struct {
	Name      string
	Signature string
	File      string
	Line      uint32
	Kind      graph.SymbolKind
}

func sigFromSymbol(s *graph.Symbol) SymbolWithSig {
	return SymbolWithSig{
		Name:      s.Name,
		Signature: s.SignatureOrFallback(),
		File:      s.File,
		Line:      s.StartLine,
		Kind:      s.Kind,
	}
}

// Assembler builds curated context from a knowledge graph.
type Assembler struct {
	Graph  *graph.Graph
	Root   string // repo root for reading file contents
	Budget Budget
}

// NewAssembler creates an assembler with the default budget.
func NewAssembler(g *graph.Graph, root string) *Assembler {
	return &Assembler{
		Graph:  g,
		Root:   root,
		Budget: DefaultBudget,
	}
}

// ForSymbol assembles context for the given symbol.
func (a *Assembler) ForSymbol(target *graph.Symbol) (*AssembledContext, error) {
	ctx := &AssembledContext{
		Language: target.Language,
		FilePath: target.File,
	}

	// --- Phase 1: Target body (always included, non-negotiable) ---
	body, err := a.readSymbolBody(target)
	if err != nil {
		return nil, err
	}
	ctx.Target = SymbolWithBody{Symbol: target, Body: body}

	// --- Phase 2: File-level metadata (cheap, always include) ---
	if fileNode, ok := a.Graph.Files[target.File]; ok {
		ctx.Imports = fileNode.Imports
	}

	// --- Phase 3: Call graph — signatures only, very cheap ---
	ctx.Dependencies = a.collectDependencies(target)
	ctx.Dependents = a.collectDependents(target)

	// --- Phase 4: Siblings — other symbols in the same file ---
	ctx.Siblings = a.collectSiblings(target)

	// --- Phase 5: Examples — similar patterns (full bodies, expensive) ---
	ctx.Examples = a.findExamples(target)

	// --- Phase 6: Tests (full bodies, expensive) ---
	ctx.Tests = a.findTests(target)

	// --- Budget enforcement ---
	ctx.TokenEstimate = a.estimateTokens(ctx)
	if ctx.TokenEstimate > a.Budget.MaxTokens {
		a.trim(ctx)
	}

	return ctx, nil
}

// --- Collection methods ---

func (a *Assembler) collectDependencies(target *graph.Symbol) []SymbolWithSig {
	depIDs := a.Graph.Dependencies(target.ID)
	return a.idsToSigs(depIDs, target.ID)
}

func (a *Assembler) collectDependents(target *graph.Symbol) []SymbolWithSig {
	depIDs := a.Graph.Dependents(target.ID)
	return a.idsToSigs(depIDs, target.ID)
}

func (a *Assembler) idsToSigs(ids []graph.SymbolID, exclude graph.SymbolID) []SymbolWithSig {
	seen := make(map[graph.SymbolID]bool)
	var result []SymbolWithSig
	for _, id := range ids {
		if id == exclude || seen[id] {
			continue
		}
		seen[id] = true
		if sym, ok := a.Graph.Symbols[id]; ok {
			result = append(result, sigFromSymbol(sym))
		}
	}
	return result
}

func (a *Assembler) collectSiblings(target *graph.Symbol) []SymbolWithSig {
	syms := a.Graph.SymbolsInFile(target.File)
	var result []SymbolWithSig
	for _, s := range syms {
		if s.ID == target.ID {
			continue
		}
		result = append(result, sigFromSymbol(s))
	}
	return result
}

// findExamples locates similar code patterns in the repo.
// Scoring: same package > same kind > name similarity > same language.
func (a *Assembler) findExamples(target *graph.Symbol) []SymbolWithBody {
	type candidate struct {
		sym   *graph.Symbol
		score int
	}

	targetPkg := ""
	if fn, ok := a.Graph.Files[target.File]; ok {
		targetPkg = fn.Package
	}

	var candidates []candidate

	for _, sym := range a.Graph.Symbols {
		// Skip self
		if sym.ID == target.ID {
			continue
		}
		// Must be same language
		if sym.Language != target.Language {
			continue
		}
		// Only functions/methods as examples (the things you'd edit)
		if sym.Kind != graph.KindFunction && sym.Kind != graph.KindMethod {
			continue
		}

		score := 0

		// Same kind bonus
		if sym.Kind == target.Kind {
			score += 2
		}

		// Same package bonus
		if targetPkg != "" {
			if fn, ok := a.Graph.Files[sym.File]; ok && fn.Package == targetPkg {
				score += 3
			}
		}

		// Name similarity: shared prefix or suffix patterns
		// e.g., HandleRequest and HandleResponse share "Handle" prefix
		score += nameSimilarity(target.Name, sym.Name)

		// Signature similarity: shared type references
		score += signatureSimilarity(target.Signature, sym.Signature)

		if score > 2 {
			candidates = append(candidates, candidate{sym: sym, score: score})
		}
	}

	// Sort by score descending
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	// Take top 3
	limit := 3
	if len(candidates) < limit {
		limit = len(candidates)
	}

	var results []SymbolWithBody
	for _, c := range candidates[:limit] {
		body, err := a.readSymbolBody(c.sym)
		if err != nil {
			continue
		}
		results = append(results, SymbolWithBody{Symbol: c.sym, Body: body})
	}

	return results
}

// findTests locates test functions that exercise the target.
func (a *Assembler) findTests(target *graph.Symbol) []SymbolWithBody {
	nameLower := strings.ToLower(target.Name)
	var tests []*graph.Symbol

	for _, sym := range a.Graph.Symbols {
		if sym.Kind != graph.KindFunction && sym.Kind != graph.KindMethod {
			continue
		}

		isTest := false
		switch sym.Language {
		case graph.LangGo:
			// TestFoo, Test_Foo, TestFoo_SubCase
			if strings.HasPrefix(sym.Name, "Test") {
				rest := strings.ToLower(strings.TrimPrefix(sym.Name, "Test"))
				rest = strings.TrimPrefix(rest, "_")
				if strings.Contains(rest, nameLower) {
					isTest = true
				}
			}
		case graph.LangPython:
			// test_foo, test_foo_bar
			if strings.HasPrefix(sym.Name, "test_") {
				rest := strings.ToLower(strings.TrimPrefix(sym.Name, "test_"))
				if strings.Contains(rest, nameLower) {
					isTest = true
				}
			}
			// Also check for methods in TestCase classes
			if sym.Kind == graph.KindMethod && strings.HasPrefix(sym.Name, "test_") {
				if strings.Contains(strings.ToLower(sym.Name), nameLower) {
					isTest = true
				}
			}
		case graph.LangTypeScript:
			// describe/it/test blocks are harder to match by name
			// Fall back to: any function containing the target name in a test file
			if strings.Contains(sym.File, "test") || strings.Contains(sym.File, "spec") {
				if strings.Contains(strings.ToLower(sym.Name), nameLower) {
					isTest = true
				}
			}
		}

		if isTest {
			tests = append(tests, sym)
		}
	}

	// Limit to 3 tests
	if len(tests) > 3 {
		tests = tests[:3]
	}

	var results []SymbolWithBody
	for _, t := range tests {
		body, err := a.readSymbolBody(t)
		if err != nil {
			continue
		}
		results = append(results, SymbolWithBody{Symbol: t, Body: body})
	}

	return results
}

// --- File reading ---

func (a *Assembler) readSymbolBody(sym *graph.Symbol) (string, error) {
	absPath := filepath.Join(a.Root, sym.File)
	content, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}
	return sym.BodyFrom(content), nil
}

// --- Similarity scoring ---

// nameSimilarity scores how similar two symbol names are.
// Rewards shared prefixes, suffixes, and camelCase/snake_case segments.
func nameSimilarity(a, b string) int {
	aParts := splitName(a)
	bParts := splitName(b)

	score := 0
	bSet := make(map[string]bool)
	for _, p := range bParts {
		bSet[strings.ToLower(p)] = true
	}
	for _, p := range aParts {
		if bSet[strings.ToLower(p)] {
			score++
		}
	}
	return score
}

// signatureSimilarity scores overlap in type names between two signatures.
func signatureSimilarity(a, b string) int {
	aTypes := extractTypeWords(a)
	bTypes := extractTypeWords(b)

	bSet := make(map[string]bool)
	for _, t := range bTypes {
		bSet[t] = true
	}

	score := 0
	for _, t := range aTypes {
		if bSet[t] {
			score++
		}
	}
	return score
}

// splitName breaks a symbol name into parts by camelCase or snake_case.
func splitName(name string) []string {
	// Handle snake_case
	if strings.Contains(name, "_") {
		parts := strings.Split(name, "_")
		var result []string
		for _, p := range parts {
			if p != "" {
				result = append(result, p)
			}
		}
		return result
	}

	// Handle camelCase / PascalCase
	var parts []string
	current := strings.Builder{}
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// extractTypeWords pulls out capitalized words from a signature that look like types.
func extractTypeWords(sig string) []string {
	var types []string
	words := strings.FieldsFunc(sig, func(r rune) bool {
		return !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	})
	for _, w := range words {
		// Type names start with uppercase (Go, TS) or are after : (TS, Python)
		if len(w) > 1 && w[0] >= 'A' && w[0] <= 'Z' {
			types = append(types, w)
		}
	}
	return types
}

// --- Budget management ---

func (a *Assembler) estimateTokens(ctx *AssembledContext) int {
	total := 0
	total += tokenEstimate(ctx.Target.Body)
	for _, d := range ctx.Dependencies {
		total += tokenEstimate(d.Signature)
	}
	for _, d := range ctx.Dependents {
		total += tokenEstimate(d.Signature)
	}
	for _, s := range ctx.Siblings {
		total += tokenEstimate(s.Signature)
	}
	for _, e := range ctx.Examples {
		total += tokenEstimate(e.Body)
	}
	for _, t := range ctx.Tests {
		total += tokenEstimate(t.Body)
	}
	// Add overhead for section headers and formatting
	total += 200
	return total
}

func tokenEstimate(s string) int {
	// Rough heuristic: ~4 chars per token for code
	return (len(s) + 3) / 4
}

// trim removes context sections from lowest to highest priority until
// within budget. Priority (highest = cut last): target > deps > dependents > examples > tests > siblings.
func (a *Assembler) trim(ctx *AssembledContext) {
	ctx.Truncated = true

	// Cut siblings first
	if a.estimateTokens(ctx) > a.Budget.MaxTokens {
		ctx.Siblings = nil
	}

	// Cut tests
	if a.estimateTokens(ctx) > a.Budget.MaxTokens {
		ctx.Tests = nil
	}

	// Trim examples: remove from the end (lowest scored)
	for a.estimateTokens(ctx) > a.Budget.MaxTokens && len(ctx.Examples) > 0 {
		ctx.Examples = ctx.Examples[:len(ctx.Examples)-1]
	}

	// Trim dependents to signatures only (they already are, but cap count)
	if a.estimateTokens(ctx) > a.Budget.MaxTokens && len(ctx.Dependents) > 5 {
		ctx.Dependents = ctx.Dependents[:5]
	}

	// Trim dependencies (unlikely to be the problem, but just in case)
	if a.estimateTokens(ctx) > a.Budget.MaxTokens && len(ctx.Dependencies) > 10 {
		ctx.Dependencies = ctx.Dependencies[:10]
	}
}
