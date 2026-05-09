package indexer

import (
	"strings"
	"unsafe"

	"github.com/julython/majordomo/internal/repomap/graph"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
)

type PythonExtractor struct{}

func (p *PythonExtractor) Language() graph.Language { return graph.LangPython }
func (p *PythonExtractor) Grammar() *tree_sitter.Language {
	return tree_sitter.NewLanguage(unsafe.Pointer(tree_sitter_python.Language()))
}

func (p *PythonExtractor) Extract(path string, content []byte, root *tree_sitter.Node) (*ExtractionResult, error) {
	result := &ExtractionResult{}
	result.Package = pythonModuleName(path)
	result.Imports = p.extractImports(root, content)

	for i := uint(0); i < root.ChildCount(); i++ {
		child := root.Child(i)
		if child == nil {
			continue
		}
		switch child.Kind() {
		case "function_definition":
			if sym := p.extractFunction(path, child, content, ""); sym != nil {
				result.Symbols = append(result.Symbols, sym)
			}
		case "class_definition":
			syms := p.extractClass(path, child, content)
			result.Symbols = append(result.Symbols, syms...)
		case "decorated_definition":
			syms := p.extractDecorated(path, child, content)
			result.Symbols = append(result.Symbols, syms...)
		case "expression_statement":
			if sym := p.extractAssignment(path, child, content); sym != nil {
				result.Symbols = append(result.Symbols, sym)
			}
		}
	}

	return result, nil
}

func (p *PythonExtractor) extractImports(root *tree_sitter.Node, content []byte) []graph.Import {
	var imports []graph.Import

	walkNodes(root, func(n *tree_sitter.Node) {
		switch n.Kind() {
		case "import_statement":
			for j := uint(0); j < n.NamedChildCount(); j++ {
				child := n.NamedChild(j)
				if child == nil {
					continue
				}
				if child.Kind() == "dotted_name" || child.Kind() == "aliased_import" {
					imp := graph.Import{
						Path: nodeText(child, content),
						Line: int(n.StartPosition().Row) + 1,
					}
					if child.Kind() == "aliased_import" {
						if name := child.ChildByFieldName("name"); name != nil {
							imp.Path = nodeText(name, content)
						}
						if alias := child.ChildByFieldName("alias"); alias != nil {
							imp.Alias = nodeText(alias, content)
						}
					}
					imports = append(imports, imp)
				}
			}
		case "import_from_statement":
			module := ""
			if modNode := n.ChildByFieldName("module_name"); modNode != nil {
				module = nodeText(modNode, content)
			}
			imports = append(imports, graph.Import{
				Path: module,
				Line: int(n.StartPosition().Row) + 1,
			})
		}
	})

	return imports
}

func (p *PythonExtractor) extractFunction(path string, node *tree_sitter.Node, content []byte, parent string) *graph.Symbol {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	name := nodeText(nameNode, content)

	kind := graph.KindFunction
	qualName := name
	if parent != "" {
		kind = graph.KindMethod
		qualName = parent + "." + name
	}

	return &graph.Symbol{
		ID:         graph.NewSymbolID(path, qualName),
		Name:       name,
		Kind:       kind,
		File:       path,
		Language:   graph.LangPython,
		StartByte:  uint32(node.StartByte()),
		EndByte:    uint32(node.EndByte()),
		StartLine:  uint32(node.StartPosition().Row) + 1,
		EndLine:    uint32(node.EndPosition().Row) + 1,
		Signature:  extractSignatureLine(node, content, "block"),
		DocComment: p.extractDocstring(node, content),
		Exported:   !strings.HasPrefix(name, "_"),
		Parent:     parent,
	}
}

func (p *PythonExtractor) extractClass(path string, node *tree_sitter.Node, content []byte) []*graph.Symbol {
	var syms []*graph.Symbol

	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	className := nodeText(nameNode, content)

	classSym := &graph.Symbol{
		ID:         graph.NewSymbolID(path, className),
		Name:       className,
		Kind:       graph.KindClass,
		File:       path,
		Language:   graph.LangPython,
		StartByte:  uint32(node.StartByte()),
		EndByte:    uint32(node.EndByte()),
		StartLine:  uint32(node.StartPosition().Row) + 1,
		EndLine:    uint32(node.EndPosition().Row) + 1,
		Signature:  extractSignatureLine(node, content, "block"),
		DocComment: p.extractDocstring(node, content),
		Exported:   !strings.HasPrefix(className, "_"),
	}
	syms = append(syms, classSym)

	body := node.ChildByFieldName("body")
	if body == nil {
		body = childByKind(node, "block")
	}
	if body != nil {
		for i := uint(0); i < body.ChildCount(); i++ {
			child := body.Child(i)
			if child == nil {
				continue
			}
			switch child.Kind() {
			case "function_definition":
				if sym := p.extractFunction(path, child, content, className); sym != nil {
					syms = append(syms, sym)
				}
			case "decorated_definition":
				inner := p.extractDecoratedInner(path, child, content, className)
				syms = append(syms, inner...)
			}
		}
	}

	return syms
}

func (p *PythonExtractor) extractDecorated(path string, node *tree_sitter.Node, content []byte) []*graph.Symbol {
	return p.extractDecoratedInner(path, node, content, "")
}

func (p *PythonExtractor) extractDecoratedInner(path string, node *tree_sitter.Node, content []byte, parent string) []*graph.Symbol {
	var syms []*graph.Symbol

	if def := childByKind(node, "function_definition"); def != nil {
		if sym := p.extractFunction(path, def, content, parent); sym != nil {
			sym.StartByte = uint32(node.StartByte())
			sym.StartLine = uint32(node.StartPosition().Row) + 1
			syms = append(syms, sym)
		}
	}

	if def := childByKind(node, "class_definition"); def != nil {
		classSubs := p.extractClass(path, def, content)
		if len(classSubs) > 0 {
			classSubs[0].StartByte = uint32(node.StartByte())
			classSubs[0].StartLine = uint32(node.StartPosition().Row) + 1
		}
		syms = append(syms, classSubs...)
	}

	return syms
}

func (p *PythonExtractor) extractAssignment(path string, node *tree_sitter.Node, content []byte) *graph.Symbol {
	if node.NamedChildCount() == 0 {
		return nil
	}

	expr := node.NamedChild(0)
	if expr == nil {
		return nil
	}

	var name string
	switch expr.Kind() {
	case "assignment":
		left := expr.ChildByFieldName("left")
		if left != nil && left.Kind() == "identifier" {
			name = nodeText(left, content)
		}
	default:
		return nil
	}

	if name == "" || !isUpperSnakeCase(name) {
		return nil
	}

	return &graph.Symbol{
		ID:        graph.NewSymbolID(path, name),
		Name:      name,
		Kind:      graph.KindConst,
		File:      path,
		Language:  graph.LangPython,
		StartByte: uint32(node.StartByte()),
		EndByte:   uint32(node.EndByte()),
		StartLine: uint32(node.StartPosition().Row) + 1,
		EndLine:   uint32(node.EndPosition().Row) + 1,
		Signature: strings.TrimSpace(nodeText(node, content)),
		Exported:  !strings.HasPrefix(name, "_"),
	}
}

func (p *PythonExtractor) extractDocstring(node *tree_sitter.Node, content []byte) string {
	doc := prevSiblingComment(node, content)

	body := node.ChildByFieldName("body")
	if body == nil {
		body = childByKind(node, "block")
	}
	if body == nil || body.NamedChildCount() == 0 {
		return doc
	}

	first := body.NamedChild(0)
	if first == nil {
		return doc
	}

	if first.Kind() == "expression_statement" && first.NamedChildCount() > 0 {
		str := first.NamedChild(0)
		if str != nil && str.Kind() == "string" {
			docstring := nodeText(str, content)
			docstring = strings.TrimPrefix(docstring, `"""`)
			docstring = strings.TrimPrefix(docstring, `'''`)
			docstring = strings.TrimSuffix(docstring, `"""`)
			docstring = strings.TrimSuffix(docstring, `'''`)
			docstring = strings.TrimSpace(docstring)
			if doc != "" {
				return doc + "\n" + docstring
			}
			return docstring
		}
	}

	return doc
}

func pythonModuleName(path string) string {
	name := strings.TrimSuffix(path, ".py")
	name = strings.TrimSuffix(name, ".pyi")
	name = strings.TrimSuffix(name, "/__init__")
	name = strings.ReplaceAll(name, "/", ".")
	return name
}

func isUpperSnakeCase(s string) bool {
	for _, r := range s {
		if r != '_' && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return len(s) > 0
}
