package indexer

import (
	"strings"
	"unicode"
	"unsafe"

	"github.com/julython/majordomo/internal/repomap/graph"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
)

type GoExtractor struct{}

func (g *GoExtractor) Language() graph.Language { return graph.LangGo }
func (g *GoExtractor) Grammar() *tree_sitter.Language {
	return tree_sitter.NewLanguage(unsafe.Pointer(tree_sitter_go.Language()))
}

func (g *GoExtractor) Extract(path string, content []byte, root *tree_sitter.Node) (*ExtractionResult, error) {
	result := &ExtractionResult{}

	// Extract package name
	if pkg := childByKind(root, "package_clause"); pkg != nil {
		if name := pkg.ChildByFieldName("name"); name != nil {
			result.Package = nodeText(name, content)
		}
	}

	// Extract imports
	result.Imports = g.extractImports(root, content)

	// Walk top-level declarations
	for i := uint(0); i < root.ChildCount(); i++ {
		child := root.Child(i)
		if child == nil {
			continue
		}
		switch child.Kind() {
		case "function_declaration":
			if sym := g.extractFunction(path, child, content); sym != nil {
				result.Symbols = append(result.Symbols, sym)
			}
		case "method_declaration":
			if sym := g.extractMethod(path, child, content); sym != nil {
				result.Symbols = append(result.Symbols, sym)
			}
		case "type_declaration":
			syms := g.extractTypeDecl(path, child, content)
			result.Symbols = append(result.Symbols, syms...)
		case "const_declaration":
			syms := g.extractConstVar(path, child, content, graph.KindConst)
			result.Symbols = append(result.Symbols, syms...)
		case "var_declaration":
			syms := g.extractConstVar(path, child, content, graph.KindVar)
			result.Symbols = append(result.Symbols, syms...)
		}
	}

	return result, nil
}

func (g *GoExtractor) extractImports(root *tree_sitter.Node, content []byte) []graph.Import {
	var imports []graph.Import

	walkNodes(root, func(n *tree_sitter.Node) {
		if n.Kind() != "import_spec" {
			return
		}

		imp := graph.Import{
			Line: int(n.StartPosition().Row) + 1,
		}

		if pathNode := n.ChildByFieldName("path"); pathNode != nil {
			imp.Path = strings.Trim(nodeText(pathNode, content), `"`)
		}
		if nameNode := n.ChildByFieldName("name"); nameNode != nil {
			imp.Alias = nodeText(nameNode, content)
		}

		if imp.Path != "" {
			imports = append(imports, imp)
		}
	})

	return imports
}

func (g *GoExtractor) extractFunction(path string, node *tree_sitter.Node, content []byte) *graph.Symbol {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	name := nodeText(nameNode, content)

	return &graph.Symbol{
		ID:         graph.NewSymbolID(path, name),
		Name:       name,
		Kind:       graph.KindFunction,
		File:       path,
		Language:   graph.LangGo,
		StartByte:  uint32(node.StartByte()),
		EndByte:    uint32(node.EndByte()),
		StartLine:  uint32(node.StartPosition().Row) + 1,
		EndLine:    uint32(node.EndPosition().Row) + 1,
		Signature:  extractSignatureLine(node, content, "block"),
		DocComment: prevSiblingComment(node, content),
		Exported:   isGoExported(name),
	}
}

func (g *GoExtractor) extractMethod(path string, node *tree_sitter.Node, content []byte) *graph.Symbol {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	name := nodeText(nameNode, content)

	// Extract receiver type
	receiver := ""
	if recv := node.ChildByFieldName("receiver"); recv != nil {
		if recv.NamedChildCount() > 0 {
			paramDecl := recv.NamedChild(0)
			recvText := nodeText(paramDecl, content)
			parts := strings.Fields(recvText)
			if len(parts) > 0 {
				last := parts[len(parts)-1]
				receiver = strings.TrimLeft(last, "*)")
				receiver = strings.TrimRight(receiver, ")")
			}
		}
	}

	qualName := name
	if receiver != "" {
		qualName = receiver + "." + name
	}

	return &graph.Symbol{
		ID:         graph.NewSymbolID(path, qualName),
		Name:       name,
		Kind:       graph.KindMethod,
		File:       path,
		Language:   graph.LangGo,
		StartByte:  uint32(node.StartByte()),
		EndByte:    uint32(node.EndByte()),
		StartLine:  uint32(node.StartPosition().Row) + 1,
		EndLine:    uint32(node.EndPosition().Row) + 1,
		Signature:  extractSignatureLine(node, content, "block"),
		DocComment: prevSiblingComment(node, content),
		Exported:   isGoExported(name),
		Receiver:   receiver,
		Parent:     receiver,
	}
}

func (g *GoExtractor) extractTypeDecl(path string, node *tree_sitter.Node, content []byte) []*graph.Symbol {
	var syms []*graph.Symbol

	for i := uint(0); i < node.NamedChildCount(); i++ {
		spec := node.NamedChild(i)
		if spec == nil || spec.Kind() != "type_spec" {
			continue
		}

		nameNode := spec.ChildByFieldName("name")
		if nameNode == nil {
			continue
		}
		name := nodeText(nameNode, content)

		kind := graph.KindType
		typeNode := spec.ChildByFieldName("type")
		if typeNode != nil {
			switch typeNode.Kind() {
			case "struct_type":
				kind = graph.KindStruct
			case "interface_type":
				kind = graph.KindInterface
			}
		}

		syms = append(syms, &graph.Symbol{
			ID:         graph.NewSymbolID(path, name),
			Name:       name,
			Kind:       kind,
			File:       path,
			Language:   graph.LangGo,
			StartByte:  uint32(spec.StartByte()),
			EndByte:    uint32(spec.EndByte()),
			StartLine:  uint32(spec.StartPosition().Row) + 1,
			EndLine:    uint32(spec.EndPosition().Row) + 1,
			Signature:  extractSignatureLine(spec, content, "struct_type"),
			DocComment: prevSiblingComment(spec, content),
			Exported:   isGoExported(name),
		})
	}

	return syms
}

func (g *GoExtractor) extractConstVar(path string, node *tree_sitter.Node, content []byte, kind graph.SymbolKind) []*graph.Symbol {
	var syms []*graph.Symbol

	walkNodes(node, func(n *tree_sitter.Node) {
		if n.Kind() != "const_spec" && n.Kind() != "var_spec" {
			return
		}
		nameNode := n.ChildByFieldName("name")
		if nameNode == nil {
			for j := uint(0); j < n.NamedChildCount(); j++ {
				child := n.NamedChild(j)
				if child != nil && child.Kind() == "identifier" {
					name := nodeText(child, content)
					syms = append(syms, &graph.Symbol{
						ID:        graph.NewSymbolID(path, name),
						Name:      name,
						Kind:      kind,
						File:      path,
						Language:  graph.LangGo,
						StartByte: uint32(n.StartByte()),
						EndByte:   uint32(n.EndByte()),
						StartLine: uint32(n.StartPosition().Row) + 1,
						EndLine:   uint32(n.EndPosition().Row) + 1,
						Signature: strings.TrimSpace(nodeText(n, content)),
						Exported:  isGoExported(name),
					})
					break
				}
			}
			return
		}
		name := nodeText(nameNode, content)
		syms = append(syms, &graph.Symbol{
			ID:        graph.NewSymbolID(path, name),
			Name:      name,
			Kind:      kind,
			File:      path,
			Language:  graph.LangGo,
			StartByte: uint32(n.StartByte()),
			EndByte:   uint32(n.EndByte()),
			StartLine: uint32(n.StartPosition().Row) + 1,
			EndLine:   uint32(n.EndPosition().Row) + 1,
			Signature: strings.TrimSpace(nodeText(n, content)),
			Exported:  isGoExported(name),
		})
	})

	return syms
}

func isGoExported(name string) bool {
	if name == "" {
		return false
	}
	return unicode.IsUpper(rune(name[0]))
}
