// Package executor provides primitives for modifying repo files and running commands.
// All symbol-based operations use exact byte ranges from the tree-sitter graph,
// so edits are surgical — no fragile text search-and-replace.
package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/julython/majordomo/internal/repomap/graph"
)

// Executor holds the repo root for constructing absolute file paths.
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
// Used for create steps — non-destructive, safe to execute.
func (e *Executor) WriteFile(relPath string, content []byte) error {
	return os.WriteFile(filepath.Join(e.Root, relPath), content, 0o644)
}

// ReplaceSymbol replaces the content between sym.StartByte and sym.EndByte
// with newBody. Path is repo-relative. Used for modify steps.
func (e *Executor) ReplaceSymbol(relPath string, sym *graph.Symbol, newBody string) error {
	content, err := e.ReadFile(relPath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	if int(sym.EndByte) > len(content) {
		return fmt.Errorf("symbol %s: EndByte %d exceeds file size %d", sym.Name, sym.EndByte, len(content))
	}

	replaced := append(content[:sym.StartByte], []byte(newBody)...)
	replaced = append(replaced, content[sym.EndByte:]...)
	return os.WriteFile(filepath.Join(e.Root, relPath), replaced, 0o644)
}

// InsertAfter inserts newCode at byte offset afterSym.EndByte.
// Path is repo-relative. Used for add steps.
func (e *Executor) InsertAfter(relPath string, afterSym *graph.Symbol, newCode string) error {
	content, err := e.ReadFile(relPath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	if int(afterSym.EndByte) > len(content) {
		return fmt.Errorf("symbol %s: EndByte %d exceeds file size %d", afterSym.Name, afterSym.EndByte, len(content))
	}

	// Insert a newline before and after if content isn't at file boundaries
	insertPos := int(afterSym.EndByte)
	if insertPos > 0 && insertPos < len(content) {
		if content[insertPos-1] != '\n' {
			insertPos = insertPos - 1
		}
	}
	if insertPos < len(content) && content[insertPos] != '\n' && content[insertPos-1] != '\n' {
		insertPos = insertPos + 1
	}

	inserted := append(content[:insertPos], append([]byte("\n"+newCode+"\n"), content[insertPos:]...)...)
	return os.WriteFile(filepath.Join(e.Root, relPath), inserted, 0o644)
}

// DeleteSymbol removes the content between sym.StartByte and sym.EndByte,
// preserving surrounding newlines. Path is repo-relative. Used for delete steps.
func (e *Executor) DeleteSymbol(relPath string, sym *graph.Symbol) error {
	content, err := e.ReadFile(relPath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	if int(sym.EndByte) > len(content) {
		return fmt.Errorf("symbol %s: EndByte %d exceeds file size %d", sym.Name, sym.EndByte, len(content))
	}

	start := int(sym.StartByte)
	end := int(sym.EndByte)

	// Trim leading newline from the range
	if start > 0 && content[start-1] == '\n' {
		start--
	}
	// Trim trailing newline from the range
	if end < len(content) && content[end] == '\n' {
		end++
	}

	deleted := append(content[:start], content[end:]...)
	return os.WriteFile(filepath.Join(e.Root, relPath), deleted, 0o644)
}

// RunCommand executes a shell command in the given directory.
// name and args are the command and its arguments.
// Returns combined stdout+stderr.
func (e *Executor) RunCommand(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// TrimLeadingWhitespace removes leading whitespace (spaces/tabs/newlines) from a string.
func TrimLeadingWhitespace(s string) string {
	return strings.TrimRight(s, "\n")
}
