package indexer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/julython/majordomo/internal/repomap/graph"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// RefStats tracks what the reference pass found.
type RefStats struct {
	FilesScanned   int
	RefsFound      int
	RefsResolved   int
	RefsUnresolved int
}

func (s RefStats) String() string {
	return fmt.Sprintf("files=%d refs_found=%d resolved=%d unresolved=%d",
		s.FilesScanned, s.RefsFound, s.RefsResolved, s.RefsUnresolved)
}

// ResolveReferences performs the second pass over all indexed files,
// extracting call/type/identifier references and resolving them against
// the symbol index. Must be called after Index() and ResolveImportEdges().
func (idx *Indexer) ResolveReferences() RefStats {
	var stats RefStats

	for relPath, fileNode := range idx.Graph.Files {
		extractor, ok := idx.extractors[fileNode.Language]
		if !ok {
			continue
		}

		absPath := filepath.Join(idx.Root, relPath)
		content, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}

		idx.parser.SetLanguage(extractor.Grammar())
		tree := idx.parser.Parse(content, nil)
		if tree == nil {
			continue
		}

		root := tree.RootNode()

		// Build a sorted list of symbols in this file for containment lookup
		fileSyms := idx.Graph.SymbolsInFile(relPath)
		sort.Slice(fileSyms, func(i, j int) bool {
			return fileSyms[i].StartByte < fileSyms[j].StartByte
		})

		// Collect raw references from the tree
		var rawRefs []rawRef
		switch fileNode.Language {
		case graph.LangGo:
			rawRefs = extractGoRefs(root, content)
		case graph.LangPython:
			rawRefs = extractPythonRefs(root, content)
		case graph.LangTypeScript:
			rawRefs = extractTSRefs(root, content)
		}

		stats.FilesScanned++
		stats.RefsFound += len(rawRefs)

		// Resolve each reference
		for _, ref := range rawRefs {
			// Find which symbol this reference lives inside
			fromSym := enclosingSymbol(fileSyms, ref.byteOffset)
			if fromSym == nil {
				// Reference at module/file level — use the file pseudo-symbol
				fromSym = &graph.Symbol{ID: graph.NewSymbolID(relPath, "")}
			}

			// Try to resolve the target
			targetID := idx.resolveRef(ref, relPath, fileNode)
			if targetID == "" {
				stats.RefsUnresolved++
				continue
			}

			// Don't add self-references
			if targetID == fromSym.ID {
				continue
			}

			idx.Graph.AddEdge(graph.Edge{
				From: fromSym.ID,
				To:   targetID,
				Kind: graph.EdgeReferences,
				File: relPath,
				Line: ref.line,
			})
			stats.RefsResolved++
		}

		tree.Close()
	}

	return stats
}

// rawRef is an unresolved reference found in source code.
type rawRef struct {
	name       string // simple name: "Foo", "handle_request"
	qualifier  string // package/object prefix: "http", "self", "pkg"
	byteOffset uint32 // where in the file (for containment)
	line       int
	kind       refKind
}

type refKind int

const (
	refCall   refKind = iota // function/method call
	refType                  // type reference
	refAccess                // attribute/field access
)

// --- Enclosing symbol lookup ---

// enclosingSymbol finds which symbol a byte offset falls inside, using
// the sorted symbol list for the file. Returns nil if outside all symbols.
func enclosingSymbol(syms []*graph.Symbol, offset uint32) *graph.Symbol {
	// Walk backwards through symbols to find the innermost enclosing one.
	// Since methods are nested inside classes (Python/TS), we want the
	// most specific (smallest range) symbol that contains the offset.
	var best *graph.Symbol
	for _, sym := range syms {
		if offset >= sym.StartByte && offset < sym.EndByte {
			if best == nil || (sym.EndByte-sym.StartByte) < (best.EndByte-best.StartByte) {
				best = sym
			}
		}
	}
	return best
}

// --- Resolution ---

// resolveRef tries to match a raw reference against the symbol index.
// Resolution order: same file → same package → imported packages → global fuzzy.
func (idx *Indexer) resolveRef(ref rawRef, fromPath string, fromFile *graph.FileNode) graph.SymbolID {
	g := idx.Graph

	if ref.qualifier != "" {
		return idx.resolveQualifiedRef(ref, fromPath, fromFile)
	}

	// 1. Same file — exact name match
	for _, sym := range g.SymbolsInFile(fromPath) {
		if sym.Name == ref.name {
			return sym.ID
		}
	}

	// 2. Same package — exact name match among other files in the package
	if fromFile.Package != "" {
		for _, sym := range g.SymbolsInPackage(fromFile.Package) {
			if sym.Name == ref.name && sym.File != fromPath {
				return sym.ID
			}
		}
	}

	// 3. Imported symbols — check if the name matches something in an imported file
	for _, imp := range fromFile.Imports {
		targetFile := idx.resolveImport(imp, fromFile.Language, fromPath)
		if targetFile == "" {
			continue
		}
		for _, sym := range g.SymbolsInFile(targetFile) {
			if sym.Name == ref.name && sym.Exported {
				return sym.ID
			}
		}
	}

	// 4. Global name match — only if unique to avoid false positives
	matches := g.LookupName(ref.name)
	if len(matches) == 1 {
		return matches[0].ID
	}

	return ""
}

// resolveQualifiedRef handles `qualifier.name` references like `http.ListenAndServe`
// or `self.handle` or `MyClass.method`.
func (idx *Indexer) resolveQualifiedRef(ref rawRef, fromPath string, fromFile *graph.FileNode) graph.SymbolID {
	g := idx.Graph

	// Try qualifier as a type/class name — look for qualifier.name as a method
	qualName := ref.qualifier + "." + ref.name
	candidates := g.LookupName(ref.name)
	for _, sym := range candidates {
		if sym.Parent == ref.qualifier {
			return sym.ID
		}
	}

	// Try qualifier as an import alias — find which package it resolves to
	for _, imp := range fromFile.Imports {
		alias := imp.Alias
		if alias == "" {
			// Derive alias from import path
			switch fromFile.Language {
			case graph.LangGo:
				parts := strings.Split(imp.Path, "/")
				alias = parts[len(parts)-1]
			case graph.LangPython:
				parts := strings.Split(imp.Path, ".")
				alias = parts[len(parts)-1]
			case graph.LangTypeScript:
				// TS imports are usually destructured, not aliased by module
				continue
			}
		}

		if alias != ref.qualifier {
			continue
		}

		// Found the import — look for the name in the target files
		targetFile := idx.resolveImport(imp, fromFile.Language, fromPath)
		if targetFile == "" {
			continue
		}
		for _, sym := range g.SymbolsInFile(targetFile) {
			if sym.Name == ref.name && sym.Exported {
				return sym.ID
			}
		}
	}

	// Qualified name as a direct symbol ID
	id := graph.NewSymbolID(fromPath, qualName)
	if _, ok := g.Symbols[id]; ok {
		return id
	}

	return ""
}

// --- Go reference extraction ---

func extractGoRefs(root *tree_sitter.Node, content []byte) []rawRef {
	var refs []rawRef

	walkNodes(root, func(n *tree_sitter.Node) {
		switch n.Kind() {
		case "call_expression":
			fn := n.ChildByFieldName("function")
			if fn == nil {
				return
			}
			ref := refFromExpr(fn, content, refCall)
			if ref != nil {
				refs = append(refs, *ref)
			}

		case "type_identifier":
			// Type references: var x MyType, func(x MyType), etc.
			// Skip if parent is type_spec (that's a definition, not reference)
			parent := n.Parent()
			if parent != nil && parent.Kind() == "type_spec" {
				nameNode := parent.ChildByFieldName("name")
				if nameNode != nil && nameNode.StartByte() == n.StartByte() {
					return // this is the definition name, not a reference
				}
			}
			name := nodeText(n, content)
			if name != "" && name != "_" {
				refs = append(refs, rawRef{
					name:       name,
					byteOffset: uint32(n.StartByte()),
					line:       int(n.StartPosition().Row) + 1,
					kind:       refType,
				})
			}

		case "qualified_type":
			// package.Type references
			pkg := n.ChildByFieldName("package")
			name := n.ChildByFieldName("name")
			if pkg != nil && name != nil {
				refs = append(refs, rawRef{
					name:       nodeText(name, content),
					qualifier:  nodeText(pkg, content),
					byteOffset: uint32(n.StartByte()),
					line:       int(n.StartPosition().Row) + 1,
					kind:       refType,
				})
			}

		case "composite_literal":
			// MyType{...} — the type part is a reference
			typeNode := n.ChildByFieldName("type")
			if typeNode != nil {
				ref := refFromExpr(typeNode, content, refType)
				if ref != nil {
					refs = append(refs, *ref)
				}
			}
		}
	})

	return refs
}

// --- Python reference extraction ---

func extractPythonRefs(root *tree_sitter.Node, content []byte) []rawRef {
	var refs []rawRef

	walkNodes(root, func(n *tree_sitter.Node) {
		switch n.Kind() {
		case "call":
			fn := n.ChildByFieldName("function")
			if fn == nil {
				return
			}
			ref := refFromExpr(fn, content, refCall)
			if ref != nil {
				// Skip self/cls calls — resolve the method name only
				if ref.qualifier == "self" || ref.qualifier == "cls" {
					ref.qualifier = ""
				}
				refs = append(refs, *ref)
			}

		case "decorator":
			// @decorator or @module.decorator
			for i := uint(0); i < n.ChildCount(); i++ {
				child := n.Child(i)
				if child == nil {
					continue
				}
				if child.Kind() == "identifier" || child.Kind() == "attribute" {
					ref := refFromExpr(child, content, refCall)
					if ref != nil {
						refs = append(refs, *ref)
					}
					break
				}
			}

		case "type":
			// Type annotations: x: MyType, def foo(x: MyType) -> RetType
			// Walk children for identifiers and attributes
			walkNodes(n, func(inner *tree_sitter.Node) {
				if inner.Kind() == "identifier" {
					name := nodeText(inner, content)
					if name != "" && !isPythonBuiltinType(name) {
						refs = append(refs, rawRef{
							name:       name,
							byteOffset: uint32(inner.StartByte()),
							line:       int(inner.StartPosition().Row) + 1,
							kind:       refType,
						})
					}
				}
				if inner.Kind() == "attribute" {
					ref := refFromExpr(inner, content, refType)
					if ref != nil {
						refs = append(refs, *ref)
					}
				}
			})
		}
	})

	return refs
}

// --- TypeScript reference extraction ---

func extractTSRefs(root *tree_sitter.Node, content []byte) []rawRef {
	var refs []rawRef

	walkNodes(root, func(n *tree_sitter.Node) {
		switch n.Kind() {
		case "call_expression":
			fn := n.ChildByFieldName("function")
			if fn == nil {
				return
			}
			ref := refFromExpr(fn, content, refCall)
			if ref != nil {
				refs = append(refs, *ref)
			}

		case "new_expression":
			constructor := n.ChildByFieldName("constructor")
			if constructor == nil {
				return
			}
			ref := refFromExpr(constructor, content, refCall)
			if ref != nil {
				ref.kind = refType // new ClassName() is a type reference
				refs = append(refs, *ref)
			}

		case "type_identifier":
			// Type references in annotations: x: MyType, extends Base
			parent := n.Parent()
			if parent != nil {
				// Skip if this is the name being defined
				pk := parent.Kind()
				if pk == "type_alias_declaration" || pk == "interface_declaration" ||
					pk == "class_declaration" || pk == "enum_declaration" {
					nameNode := parent.ChildByFieldName("name")
					if nameNode != nil && nameNode.StartByte() == n.StartByte() {
						return
					}
				}
			}
			name := nodeText(n, content)
			if name != "" && !isTSBuiltinType(name) {
				refs = append(refs, rawRef{
					name:       name,
					byteOffset: uint32(n.StartByte()),
					line:       int(n.StartPosition().Row) + 1,
					kind:       refType,
				})
			}

		case "extends_clause", "implements_clause":
			// class Foo extends Bar implements Baz
			for i := uint(0); i < n.NamedChildCount(); i++ {
				child := n.NamedChild(i)
				if child == nil {
					continue
				}
				ref := refFromExpr(child, content, refType)
				if ref != nil {
					refs = append(refs, *ref)
				}
			}
		}
	})

	return refs
}

// --- Shared helpers ---

// refFromExpr extracts a rawRef from an expression node that could be:
//   - identifier: "foo"
//   - selector_expression / attribute / member_expression: "pkg.Foo"
func refFromExpr(node *tree_sitter.Node, content []byte, kind refKind) *rawRef {
	if node == nil {
		return nil
	}

	switch node.Kind() {
	case "identifier", "type_identifier":
		name := nodeText(node, content)
		if name == "" || name == "_" {
			return nil
		}
		return &rawRef{
			name:       name,
			byteOffset: uint32(node.StartByte()),
			line:       int(node.StartPosition().Row) + 1,
			kind:       kind,
		}

	case "selector_expression":
		// Go: operand.field
		operand := node.ChildByFieldName("operand")
		field := node.ChildByFieldName("field")
		if operand != nil && field != nil {
			qualifier := ""
			if operand.Kind() == "identifier" {
				qualifier = nodeText(operand, content)
			}
			return &rawRef{
				name:       nodeText(field, content),
				qualifier:  qualifier,
				byteOffset: uint32(node.StartByte()),
				line:       int(node.StartPosition().Row) + 1,
				kind:       kind,
			}
		}

	case "attribute":
		// Python: object.attribute
		obj := node.ChildByFieldName("object")
		attr := node.ChildByFieldName("attribute")
		if obj != nil && attr != nil {
			qualifier := ""
			if obj.Kind() == "identifier" {
				qualifier = nodeText(obj, content)
			}
			return &rawRef{
				name:       nodeText(attr, content),
				qualifier:  qualifier,
				byteOffset: uint32(node.StartByte()),
				line:       int(node.StartPosition().Row) + 1,
				kind:       kind,
			}
		}

	case "member_expression":
		// TypeScript: object.property
		obj := node.ChildByFieldName("object")
		prop := node.ChildByFieldName("property")
		if obj != nil && prop != nil {
			qualifier := ""
			if obj.Kind() == "identifier" {
				qualifier = nodeText(obj, content)
			}
			return &rawRef{
				name:       nodeText(prop, content),
				qualifier:  qualifier,
				byteOffset: uint32(node.StartByte()),
				line:       int(node.StartPosition().Row) + 1,
				kind:       kind,
			}
		}
	}

	return nil
}

// --- Builtin filters ---

func isPythonBuiltinType(name string) bool {
	builtins := map[string]bool{
		"int": true, "float": true, "str": true, "bool": true,
		"bytes": true, "list": true, "dict": true, "set": true,
		"tuple": true, "None": true, "type": true, "object": true,
		"Any": true, "Optional": true, "Union": true, "List": true,
		"Dict": true, "Set": true, "Tuple": true, "Type": true,
		"Callable": true, "Iterator": true, "Generator": true,
		"Sequence": true, "Mapping": true, "Iterable": true,
		"ClassVar": true, "Final": true, "Literal": true,
	}
	return builtins[name]
}

func isTSBuiltinType(name string) bool {
	builtins := map[string]bool{
		"string": true, "number": true, "boolean": true, "void": true,
		"any": true, "unknown": true, "never": true, "undefined": true,
		"null": true, "object": true, "symbol": true, "bigint": true,
		"Array": true, "Promise": true, "Record": true, "Partial": true,
		"Required": true, "Readonly": true, "Pick": true, "Omit": true,
		"Exclude": true, "Extract": true, "ReturnType": true,
		"Parameters": true, "ConstructorParameters": true,
		"InstanceType": true, "ThisType": true, "Map": true, "Set": true,
		"WeakMap": true, "WeakSet": true, "ReadonlyArray": true,
	}
	return builtins[name]
}
