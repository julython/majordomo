package graph

import (
	"fmt"
	"strings"
)

// SymbolID uniquely identifies a symbol within the graph.
// Format: "filepath::name" or "filepath::receiver.name" for methods.
type SymbolID string

func NewSymbolID(file, name string) SymbolID {
	return SymbolID(fmt.Sprintf("%s::%s", file, name))
}

type Language int

const (
	LangUnknown Language = iota
	LangGo
	LangPython
	LangTypeScript
)

func (l Language) String() string {
	switch l {
	case LangGo:
		return "go"
	case LangPython:
		return "python"
	case LangTypeScript:
		return "typescript"
	default:
		return "unknown"
	}
}

type SymbolKind int

const (
	KindFunction SymbolKind = iota
	KindMethod
	KindType
	KindInterface
	KindStruct
	KindClass
	KindConst
	KindVar
	KindModule // Python module-level, TS namespace
)

func (k SymbolKind) String() string {
	names := [...]string{
		"function", "method", "type", "interface",
		"struct", "class", "const", "var", "module",
	}
	if int(k) < len(names) {
		return names[k]
	}
	return "unknown"
}

type EdgeKind int

const (
	EdgeImports EdgeKind = iota
	EdgeDefines
	EdgeReferences
	EdgeImplements
	EdgeTests
	EdgeCoChanges
)

func (e EdgeKind) String() string {
	names := [...]string{
		"imports", "defines", "references",
		"implements", "tests", "co_changes",
	}
	if int(e) < len(names) {
		return names[e]
	}
	return "unknown"
}

// FileNode represents a single source file in the repository.
type FileNode struct {
	Path     string
	Language Language
	Package  string   // Go package, Python module, TS module path
	Symbols  []SymbolID
	Imports  []Import
	Hash     string // content hash for incremental rebuild
}

// Import represents a single import statement.
type Import struct {
	Path  string // "net/http", "os.path", "./utils"
	Alias string // rename alias, empty if none
	Line  int
}

// Symbol represents a named code entity with its exact location.
type Symbol struct {
	ID         SymbolID
	Name       string
	Kind       SymbolKind
	File       string
	Language   Language
	StartByte  uint32
	EndByte    uint32
	StartLine  uint32
	EndLine    uint32
	Signature  string // e.g., "func (s *Server) Handle(ctx context.Context) error"
	DocComment string // godoc, docstring, jsdoc
	Exported   bool
	Receiver   string // Go methods only: the receiver type name
	Parent     string // enclosing class/type for methods
}

// BodyFrom extracts the full source of this symbol from file content.
func (s *Symbol) BodyFrom(content []byte) string {
	if int(s.EndByte) > len(content) {
		return ""
	}
	return string(content[s.StartByte:s.EndByte])
}

// SignatureOrFallback returns the signature, or synthesizes one from name + kind.
func (s *Symbol) SignatureOrFallback() string {
	if s.Signature != "" {
		return s.Signature
	}
	return fmt.Sprintf("%s %s", s.Kind, s.Name)
}

// Edge represents a relationship between two symbols or files.
type Edge struct {
	From SymbolID
	To   SymbolID
	Kind EdgeKind
	File string // source file where this edge was found
	Line int    // line number where the reference occurs
}

// String returns a human-readable representation.
func (s *Symbol) String() string {
	var b strings.Builder
	if s.Exported {
		b.WriteString("[exported] ")
	}
	b.WriteString(s.Kind.String())
	b.WriteString(" ")
	if s.Receiver != "" {
		b.WriteString("(")
		b.WriteString(s.Receiver)
		b.WriteString(").")
	}
	b.WriteString(s.Name)
	b.WriteString(" @ ")
	b.WriteString(fmt.Sprintf("%s:%d-%d", s.File, s.StartLine, s.EndLine))
	return b.String()
}
