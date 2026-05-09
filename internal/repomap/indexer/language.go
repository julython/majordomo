package indexer

import (
	"path/filepath"
	"strings"

	"github.com/julython/majordomo/internal/repomap/graph"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// Extractor knows how to pull symbols from a parsed tree for a specific language.
type Extractor interface {
	Language() graph.Language
	Grammar() *tree_sitter.Language
	Extract(path string, content []byte, tree *tree_sitter.Node) (*ExtractionResult, error)
}

// ExtractionResult holds everything extracted from a single file.
type ExtractionResult struct {
	Package string
	Symbols []*graph.Symbol
	Imports []graph.Import
}

// DetectLanguage identifies the language from a file path.
func DetectLanguage(path string) graph.Language {
	ext := strings.ToLower(filepath.Ext(path))
	base := filepath.Base(path)

	switch ext {
	case ".go":
		return graph.LangGo
	case ".py", ".pyi":
		return graph.LangPython
	case ".ts", ".tsx":
		if strings.HasSuffix(base, ".d.ts") {
			return graph.LangUnknown
		}
		return graph.LangTypeScript
	case ".js", ".jsx":
		return graph.LangTypeScript
	default:
		return graph.LangUnknown
	}
}

// ShouldSkipDir returns true for directories that should never be entered.
func ShouldSkipDir(name string) bool {
	skip := map[string]bool{
		".git":          true,
		"node_modules":  true,
		"vendor":        true,
		"__pycache__":   true,
		".mypy_cache":   true,
		".pytest_cache": true,
		".tox":          true,
		".venv":         true,
		"venv":          true,
		"dist":          true,
		"build":         true,
		".next":         true,
		".nuxt":         true,
		"coverage":      true,
		".eggs":         true,
		".terraform":    true,
	}
	return skip[name]
}

// --- Tree-sitter helpers used by all extractors ---

// childByKind finds the first direct child with the given node kind.
func childByKind(node *tree_sitter.Node, kind string) *tree_sitter.Node {
	for i := uint(0); i < node.ChildCount(); i++ {
		child := node.Child(i)
		if child != nil && child.Kind() == kind {
			return child
		}
	}
	return nil
}

// nodeText extracts the source text for a node.
func nodeText(node *tree_sitter.Node, content []byte) string {
	start := node.StartByte()
	end := node.EndByte()
	if end > uint(len(content)) {
		end = uint(len(content))
	}
	return string(content[start:end])
}

// prevSiblingComment walks backwards from a node to collect preceding comment lines.
func prevSiblingComment(node *tree_sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}

	var comments []string
	prev := node.PrevSibling()
	for prev != nil {
		k := prev.Kind()
		if k == "comment" || k == "line_comment" || k == "block_comment" {
			comments = append([]string{strings.TrimSpace(nodeText(prev, content))}, comments...)
			prev = prev.PrevSibling()
		} else {
			break
		}
	}
	return strings.Join(comments, "\n")
}

// walkNodes calls fn for every node in the tree (depth-first).
func walkNodes(node *tree_sitter.Node, fn func(*tree_sitter.Node)) {
	fn(node)
	for i := uint(0); i < node.ChildCount(); i++ {
		child := node.Child(i)
		if child != nil {
			walkNodes(child, fn)
		}
	}
}

// extractSignatureLine extracts text from the start of a node up to (but not including)
// the body block. This gives us the function signature without the body.
func extractSignatureLine(node *tree_sitter.Node, content []byte, bodyKind string) string {
	body := childByKind(node, bodyKind)
	if body == nil {
		text := nodeText(node, content)
		if idx := strings.IndexByte(text, '\n'); idx != -1 {
			return strings.TrimSpace(text[:idx])
		}
		return strings.TrimSpace(text)
	}
	sig := string(content[node.StartByte():body.StartByte()])
	sig = strings.TrimRight(sig, " \t\n{")
	return strings.TrimSpace(sig)
}
