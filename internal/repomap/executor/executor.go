// Package executor provides primitives for modifying repo files and running commands.
// All symbol-based operations use exact byte ranges from the tree-sitter graph,
// so edits are surgical — no fragile text search-and-replace.
//
// Before each tool call that modifies files, the executor re-indexes the repo
// to ensure byte ranges are current. This is safe because tree-sitter is fast.
package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/julython/majordomo/internal/repomap/graph"
	"github.com/julython/majordomo/internal/repomap/indexer"
)

// Executor holds the repo root and performs indexed file modifications.
// It owns its graph lifecycle — re-indexing before each operation.
type Executor struct {
	Root string
}

// New creates an executor for the given repo root.
func New(repoRoot string) *Executor {
	return &Executor{Root: repoRoot}
}

// ReadFile reads the raw content of a file (path is repo-relative).
func (e *Executor) ReadFile(relPath string) ([]byte, error) {
	return os.ReadFile(filepath.Join(e.Root, relPath))
}

// WriteFile writes complete file content. Path is repo-relative.
func (e *Executor) WriteFile(relPath string, content []byte) error {
	return os.WriteFile(filepath.Join(e.Root, relPath), content, 0o644)
}

// ReplaceSymbol re-indexes the repo, looks up the symbol by name,
// and replaces its content. Returns the old symbol body for reference.
func (e *Executor) ReplaceSymbol(file, symbolName, newBody string) (string, error) {
	idx, g, err := e.Index()
	if err != nil {
		return "", err
	}
	defer idx.Close()

	sym, err := e.ResolveSymbol(g, file, symbolName)
	if err != nil {
		return "", err
	}

	content, err := e.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	if int(sym.EndByte) > len(content) {
		return "", fmt.Errorf("symbol %s: EndByte %d exceeds file size %d", symbolName, sym.EndByte, len(content))
	}

	oldBody := string(content[sym.StartByte:sym.EndByte])
	post := make([]byte, len(content)-int(sym.EndByte))
	copy(post, content[sym.EndByte:])
	replaced := make([]byte, 0, len(content)+len(newBody)-int(sym.EndByte-sym.StartByte))
	replaced = append(replaced, content[:sym.StartByte]...)
	replaced = append(replaced, []byte(newBody)...)
	replaced = append(replaced, post...)
	if err := os.WriteFile(filepath.Join(e.Root, file), replaced, 0o644); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}

	return oldBody, nil
}

// InsertAfter re-indexes the repo, looks up the symbol by name,
// and inserts new code after it.
func (e *Executor) InsertAfter(file, afterSymbol, newCode string) error {
	idx, g, err := e.Index()
	if err != nil {
		return err
	}
	defer idx.Close()

	sym, err := e.ResolveSymbol(g, file, afterSymbol)
	if err != nil {
		return err
	}

	content, err := e.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	if int(sym.EndByte) > len(content) {
		return fmt.Errorf("symbol %s: EndByte %d exceeds file size %d", afterSymbol, sym.EndByte, len(content))
	}

	insertPos := int(sym.EndByte)
	if insertPos > 0 && insertPos < len(content) {
		if content[insertPos-1] != '\n' {
			insertPos--
		}
	}
	if insertPos < len(content) && content[insertPos] != '\n' && content[insertPos-1] != '\n' {
		insertPos++
	}

	inserted := make([]byte, 0, len(content)+len("\n"+newCode+"\n"))
	inserted = append(inserted, content[:insertPos]...)
	inserted = append(inserted, []byte("\n"+newCode+"\n")...)
	inserted = append(inserted, content[insertPos:]...)
	return os.WriteFile(filepath.Join(e.Root, file), inserted, 0o644)
}

// DeleteSymbol re-indexes the repo, looks up the symbol by name,
// and removes it from the file.
func (e *Executor) DeleteSymbol(file, symbolName string) error {
	idx, g, err := e.Index()
	if err != nil {
		return err
	}
	defer idx.Close()

	sym, err := e.ResolveSymbol(g, file, symbolName)
	if err != nil {
		return err
	}

	content, err := e.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	if int(sym.EndByte) > len(content) {
		return fmt.Errorf("symbol %s: EndByte %d exceeds file size %d", symbolName, sym.EndByte, len(content))
	}

	start := int(sym.StartByte)
	end := int(sym.EndByte)

	if start > 0 && content[start-1] == '\n' {
		start--
	}
	if end < len(content) && content[end] == '\n' {
		end++
	}

	deleted := append(content[:start], content[end:]...)
	return os.WriteFile(filepath.Join(e.Root, file), deleted, 0o644)
}

// SymbolContent returns the source of a symbol by name, re-indexing first.
func (e *Executor) SymbolContent(file, symbolName string) (string, error) {
	idx, g, err := e.Index()
	if err != nil {
		return "", err
	}
	defer idx.Close()

	sym, err := e.ResolveSymbol(g, file, symbolName)
	if err != nil {
		return "", err
	}

	content, err := e.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	if int(sym.EndByte) > len(content) {
		return "", fmt.Errorf("symbol %s: EndByte %d exceeds file size %d", symbolName, sym.EndByte, len(content))
	}

	return string(content[sym.StartByte:sym.EndByte]), nil
}

// RunCommand executes a shell command in the given directory.
func (e *Executor) RunCommand(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// TrimNewlines removes trailing newlines from a string.
func TrimNewlines(s string) string {
	return strings.TrimRight(s, "\n")
}

// index returns a fresh indexed graph. The caller must close the indexer.
// Each tool call re-indexes to guarantee correct byte ranges after prior edits.
func (e *Executor) Index() (*indexer.Indexer, *graph.Graph, error) {
	idx := indexer.New(e.Root)
	if err := idx.Index(); err != nil {
		idx.Close()
		return nil, nil, fmt.Errorf("index: %w", err)
	}
	idx.ResolveImportEdges()
	idx.ResolveReferences()
	return idx, idx.Graph, nil
}

// resolveSymbol looks up a symbol by name in the given graph,
// optionally scoped to a specific file.
func (e *Executor) ResolveSymbol(g *graph.Graph, file, name string) (*graph.Symbol, error) {
	if file != "" {
		for _, sym := range g.SymbolsInFile(file) {
			if sym.Name == name {
				return sym, nil
			}
		}
	}

	matches := g.LookupName(name)
	if len(matches) > 0 {
		if file != "" {
			for _, m := range matches {
				if m.File == file {
					return m, nil
				}
			}
		}
		return matches[0], nil
	}

	fuzzy := g.FuzzyLookup(name)
	if len(fuzzy) > 0 {
		return fuzzy[0], nil
	}

	return nil, fmt.Errorf("symbol %q not found", name)
}
