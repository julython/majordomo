package graph

import (
	"fmt"
	"strings"
	"sync"
)

// Graph is the in-memory knowledge graph for a repository.
// All lookups are O(1) via pre-built indexes.
type Graph struct {
	mu sync.RWMutex

	// Primary storage
	Files   map[string]*FileNode   // keyed by repo-relative path
	Symbols map[SymbolID]*Symbol
	Edges   []Edge

	// Indexes — rebuilt on mutation
	byName      map[string][]*Symbol    // "HandleRequest" → all symbols with that name
	byPackage   map[string][]*Symbol    // "api/handlers" → all symbols in that package
	byFile      map[string][]*Symbol    // "api/handler.go" → symbols in file
	byKind      map[SymbolKind][]*Symbol
	dependents  map[SymbolID][]SymbolID // reverse edges: who references this?
	deps        map[SymbolID][]SymbolID // forward edges: what does this reference?
	edgesByFrom map[SymbolID][]Edge
	edgesByTo   map[SymbolID][]Edge

	// Repo root for relative path resolution
	Root string
}

// New creates an empty graph rooted at the given directory.
func New(root string) *Graph {
	return &Graph{
		Root:        root,
		Files:       make(map[string]*FileNode),
		Symbols:     make(map[SymbolID]*Symbol),
		byName:      make(map[string][]*Symbol),
		byPackage:   make(map[string][]*Symbol),
		byFile:      make(map[string][]*Symbol),
		byKind:      make(map[SymbolKind][]*Symbol),
		dependents:  make(map[SymbolID][]SymbolID),
		deps:        make(map[SymbolID][]SymbolID),
		edgesByFrom: make(map[SymbolID][]Edge),
		edgesByTo:   make(map[SymbolID][]Edge),
	}
}

// AddFile registers a file in the graph.
func (g *Graph) AddFile(f *FileNode) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Files[f.Path] = f
}

// AddSymbol registers a symbol and updates all indexes.
func (g *Graph) AddSymbol(s *Symbol) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.Symbols[s.ID] = s

	// Update indexes
	g.byName[s.Name] = append(g.byName[s.Name], s)
	g.byFile[s.File] = append(g.byFile[s.File], s)
	g.byKind[s.Kind] = append(g.byKind[s.Kind], s)

	// Package index: derive from file node if available
	if fn, ok := g.Files[s.File]; ok && fn.Package != "" {
		g.byPackage[fn.Package] = append(g.byPackage[fn.Package], s)
	}
}

// AddEdge registers a relationship between symbols.
func (g *Graph) AddEdge(e Edge) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.Edges = append(g.Edges, e)
	g.edgesByFrom[e.From] = append(g.edgesByFrom[e.From], e)
	g.edgesByTo[e.To] = append(g.edgesByTo[e.To], e)

	if e.Kind == EdgeReferences {
		g.deps[e.From] = append(g.deps[e.From], e.To)
		g.dependents[e.To] = append(g.dependents[e.To], e.From)
	}
}

// RemoveFile purges a file and all its symbols/edges from the graph.
// Used for incremental re-indexing.
func (g *Graph) RemoveFile(path string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	fn, ok := g.Files[path]
	if !ok {
		return
	}

	// Collect symbol IDs to remove
	remove := make(map[SymbolID]bool)
	for _, sid := range fn.Symbols {
		remove[sid] = true
		delete(g.Symbols, sid)
	}

	// Remove from name index
	for name, syms := range g.byName {
		g.byName[name] = filterSymbols(syms, remove)
		if len(g.byName[name]) == 0 {
			delete(g.byName, name)
		}
	}

	// Remove from package index
	for pkg, syms := range g.byPackage {
		g.byPackage[pkg] = filterSymbols(syms, remove)
		if len(g.byPackage[pkg]) == 0 {
			delete(g.byPackage, pkg)
		}
	}

	// Remove from kind index
	for kind, syms := range g.byKind {
		g.byKind[kind] = filterSymbols(syms, remove)
		if len(g.byKind[kind]) == 0 {
			delete(g.byKind, kind)
		}
	}

	delete(g.byFile, path)

	// Remove edges involving these symbols
	g.Edges = filterEdges(g.Edges, remove)
	for sid := range remove {
		delete(g.edgesByFrom, sid)
		delete(g.edgesByTo, sid)
		delete(g.deps, sid)
		delete(g.dependents, sid)
	}
	// Rebuild edge indexes for remaining edges referencing removed symbols
	for sid, edges := range g.edgesByFrom {
		g.edgesByFrom[sid] = filterEdgeList(edges, remove)
	}
	for sid, edges := range g.edgesByTo {
		g.edgesByTo[sid] = filterEdgeList(edges, remove)
	}

	delete(g.Files, path)
}

// --- Lookup methods ---

// LookupName returns all symbols matching the given name (case-sensitive).
func (g *Graph) LookupName(name string) []*Symbol {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.byName[name]
}

// FuzzyLookup returns symbols whose name contains the query (case-insensitive).
func (g *Graph) FuzzyLookup(query string) []*Symbol {
	g.mu.RLock()
	defer g.mu.RUnlock()

	query = strings.ToLower(query)
	var results []*Symbol
	for name, syms := range g.byName {
		if strings.Contains(strings.ToLower(name), query) {
			results = append(results, syms...)
		}
	}
	return results
}

// SymbolsInFile returns all symbols defined in the given file.
func (g *Graph) SymbolsInFile(path string) []*Symbol {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.byFile[path]
}

// SymbolsInPackage returns all symbols in the given package/module.
func (g *Graph) SymbolsInPackage(pkg string) []*Symbol {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.byPackage[pkg]
}

// Dependents returns symbol IDs that reference the given symbol.
func (g *Graph) Dependents(id SymbolID) []SymbolID {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.dependents[id]
}

// Dependencies returns symbol IDs that the given symbol references.
func (g *Graph) Dependencies(id SymbolID) []SymbolID {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.deps[id]
}

// Stats returns a summary of the graph contents.
func (g *Graph) Stats() GraphStats {
	g.mu.RLock()
	defer g.mu.RUnlock()

	byLang := make(map[Language]int)
	for _, f := range g.Files {
		byLang[f.Language]++
	}

	byKind := make(map[SymbolKind]int)
	for _, s := range g.Symbols {
		byKind[s.Kind]++
	}

	return GraphStats{
		Files:       len(g.Files),
		Symbols:     len(g.Symbols),
		Edges:       len(g.Edges),
		ByLanguage:  byLang,
		ByKind:      byKind,
	}
}

type GraphStats struct {
	Files      int
	Symbols    int
	Edges      int
	ByLanguage map[Language]int
	ByKind     map[SymbolKind]int
}

func (s GraphStats) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Files: %d, Symbols: %d, Edges: %d\n", s.Files, s.Symbols, s.Edges)
	b.WriteString("Languages: ")
	for lang, count := range s.ByLanguage {
		fmt.Fprintf(&b, "%s=%d ", lang, count)
	}
	b.WriteString("\nKinds: ")
	for kind, count := range s.ByKind {
		fmt.Fprintf(&b, "%s=%d ", kind, count)
	}
	return b.String()
}

// --- helpers ---

func filterSymbols(syms []*Symbol, remove map[SymbolID]bool) []*Symbol {
	n := 0
	for _, s := range syms {
		if !remove[s.ID] {
			syms[n] = s
			n++
		}
	}
	return syms[:n]
}

func filterEdges(edges []Edge, remove map[SymbolID]bool) []Edge {
	n := 0
	for _, e := range edges {
		if !remove[e.From] && !remove[e.To] {
			edges[n] = e
			n++
		}
	}
	return edges[:n]
}

func filterEdgeList(edges []Edge, remove map[SymbolID]bool) []Edge {
	n := 0
	for _, e := range edges {
		if !remove[e.From] && !remove[e.To] {
			edges[n] = e
			n++
		}
	}
	return edges[:n]
}
