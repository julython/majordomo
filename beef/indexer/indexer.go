package indexer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/julython/repomap/graph"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// Indexer walks a repository and builds a knowledge graph.
type Indexer struct {
	Root       string
	Graph      *graph.Graph
	extractors map[graph.Language]Extractor
	parser     *tree_sitter.Parser

	// Stats
	FilesScanned  int
	FilesSkipped  int
	ParseErrors   int
	IndexDuration time.Duration
}

// New creates an indexer for the given repo root.
func New(root string) *Indexer {
	idx := &Indexer{
		Root:       root,
		Graph:      graph.New(root),
		extractors: make(map[graph.Language]Extractor),
		parser:     tree_sitter.NewParser(),
	}

	idx.extractors[graph.LangGo] = &GoExtractor{}
	idx.extractors[graph.LangPython] = &PythonExtractor{}
	idx.extractors[graph.LangTypeScript] = &TypeScriptExtractor{}

	return idx
}

// Close frees the underlying C parser memory. Must be called when done.
func (idx *Indexer) Close() {
	idx.parser.Close()
}

// Index walks the repo and builds the full knowledge graph.
func (idx *Indexer) Index() error {
	start := time.Now()

	err := filepath.WalkDir(idx.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		if d.IsDir() {
			if ShouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, err := filepath.Rel(idx.Root, path)
		if err != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)

		lang := DetectLanguage(relPath)
		if lang == graph.LangUnknown {
			idx.FilesSkipped++
			return nil
		}

		if err := idx.indexFile(relPath, lang); err != nil {
			idx.ParseErrors++
			fmt.Fprintf(os.Stderr, "warn: %s: %v\n", relPath, err)
		}

		return nil
	})

	idx.IndexDuration = time.Since(start)
	return err
}

// IndexFile indexes (or re-indexes) a single file. Used for incremental updates.
func (idx *Indexer) IndexFile(relPath string) error {
	lang := DetectLanguage(relPath)
	if lang == graph.LangUnknown {
		return fmt.Errorf("unsupported language: %s", relPath)
	}

	idx.Graph.RemoveFile(relPath)
	return idx.indexFile(relPath, lang)
}

func (idx *Indexer) indexFile(relPath string, lang graph.Language) error {
	absPath := filepath.Join(idx.Root, relPath)
	content, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}

	hash := contentHash(content)

	// Check if file is unchanged
	if existing, ok := idx.Graph.Files[relPath]; ok {
		if existing.Hash == hash {
			return nil
		}
		idx.Graph.RemoveFile(relPath)
	}

	extractor, ok := idx.extractors[lang]
	if !ok {
		return fmt.Errorf("no extractor for %s", lang)
	}

	// Parse with tree-sitter (official API)
	idx.parser.SetLanguage(extractor.Grammar())
	tree := idx.parser.Parse(content, nil)
	if tree == nil {
		return fmt.Errorf("parse returned nil tree")
	}
	defer tree.Close()

	root := tree.RootNode()
	if root.HasError() {
		idx.ParseErrors++
	}

	result, err := extractor.Extract(relPath, content, root)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	fileNode := &graph.FileNode{
		Path:     relPath,
		Language: lang,
		Package:  result.Package,
		Imports:  result.Imports,
		Hash:     hash,
	}

	idx.Graph.AddFile(fileNode)

	for _, sym := range result.Symbols {
		fileNode.Symbols = append(fileNode.Symbols, sym.ID)
		idx.Graph.AddSymbol(sym)

		idx.Graph.AddEdge(graph.Edge{
			From: graph.NewSymbolID(relPath, ""),
			To:   sym.ID,
			Kind: graph.EdgeDefines,
			File: relPath,
			Line: int(sym.StartLine),
		})
	}

	idx.FilesScanned++
	return nil
}

// ResolveImportEdges walks all files and creates import edges between files.
// Call this after the full index pass so all files are registered.
func (idx *Indexer) ResolveImportEdges() {
	for path, file := range idx.Graph.Files {
		for _, imp := range file.Imports {
			target := idx.resolveImport(imp, file.Language, path)
			if target == "" {
				continue
			}

			idx.Graph.AddEdge(graph.Edge{
				From: graph.NewSymbolID(path, ""),
				To:   graph.NewSymbolID(target, ""),
				Kind: graph.EdgeImports,
				File: path,
				Line: imp.Line,
			})
		}
	}
}

func (idx *Indexer) resolveImport(imp graph.Import, lang graph.Language, fromPath string) string {
	switch lang {
	case graph.LangGo:
		return idx.resolveGoImport(imp)
	case graph.LangPython:
		return idx.resolvePythonImport(imp, fromPath)
	case graph.LangTypeScript:
		return idx.resolveTSImport(imp, fromPath)
	default:
		return ""
	}
}

func (idx *Indexer) resolveGoImport(imp graph.Import) string {
	parts := strings.Split(imp.Path, "/")
	if len(parts) == 0 {
		return ""
	}
	lastPart := parts[len(parts)-1]

	for fpath, fnode := range idx.Graph.Files {
		if fnode.Package == lastPart {
			dir := filepath.Dir(fpath)
			if strings.HasSuffix(filepath.ToSlash(dir), lastPart) || dir == lastPart {
				return fpath
			}
		}
	}
	return ""
}

func (idx *Indexer) resolvePythonImport(imp graph.Import, fromPath string) string {
	candidate := strings.ReplaceAll(imp.Path, ".", "/")

	for _, suffix := range []string{".py", ".pyi"} {
		if _, ok := idx.Graph.Files[candidate+suffix]; ok {
			return candidate + suffix
		}
	}
	init := candidate + "/__init__.py"
	if _, ok := idx.Graph.Files[init]; ok {
		return init
	}

	dir := filepath.Dir(fromPath)
	relCandidate := filepath.ToSlash(filepath.Join(dir, candidate))
	for _, suffix := range []string{".py", ".pyi"} {
		if _, ok := idx.Graph.Files[relCandidate+suffix]; ok {
			return relCandidate + suffix
		}
	}

	return ""
}

func (idx *Indexer) resolveTSImport(imp graph.Import, fromPath string) string {
	importPath := imp.Path

	if strings.HasPrefix(importPath, ".") {
		dir := filepath.Dir(fromPath)
		resolved := filepath.ToSlash(filepath.Join(dir, importPath))

		for _, suffix := range []string{".ts", ".tsx", ".js", ".jsx", "/index.ts", "/index.tsx", "/index.js"} {
			candidate := resolved + suffix
			if _, ok := idx.Graph.Files[candidate]; ok {
				return candidate
			}
		}
		if _, ok := idx.Graph.Files[resolved]; ok {
			return resolved
		}
	}

	return ""
}

func contentHash(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:8])
}
