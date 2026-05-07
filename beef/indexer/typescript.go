package indexer

import (
	"strings"
	"unsafe"

	"github.com/julython/repomap/graph"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

type TypeScriptExtractor struct{}

func (t *TypeScriptExtractor) Language() graph.Language { return graph.LangTypeScript }
func (t *TypeScriptExtractor) Grammar() *tree_sitter.Language {
	return tree_sitter.NewLanguage(unsafe.Pointer(tree_sitter_typescript.LanguageTypescript()))
}

func (t *TypeScriptExtractor) Extract(path string, content []byte, root *tree_sitter.Node) (*ExtractionResult, error) {
	result := &ExtractionResult{}
	result.Package = tsModuleName(path)
	result.Imports = t.extractImports(root, content)

	for i := uint(0); i < root.ChildCount(); i++ {
		child := root.Child(i)
		if child == nil {
			continue
		}
		syms := t.extractNode(path, child, content, "", false)
		result.Symbols = append(result.Symbols, syms...)
	}

	return result, nil
}

func (t *TypeScriptExtractor) extractNode(path string, node *tree_sitter.Node, content []byte, parent string, exported bool) []*graph.Symbol {
	var syms []*graph.Symbol

	switch node.Kind() {
	case "export_statement":
		if decl := node.ChildByFieldName("declaration"); decl != nil {
			return t.extractNode(path, decl, content, parent, true)
		}
		if def := childByKind(node, "function_declaration"); def != nil {
			return t.extractNode(path, def, content, parent, true)
		}
		if def := childByKind(node, "class_declaration"); def != nil {
			return t.extractNode(path, def, content, parent, true)
		}

	case "function_declaration", "generator_function_declaration":
		if sym := t.extractFunction(path, node, content, parent, exported); sym != nil {
			syms = append(syms, sym)
		}

	case "class_declaration", "abstract_class_declaration":
		syms = append(syms, t.extractClass(path, node, content, exported)...)

	case "interface_declaration":
		if sym := t.extractInterface(path, node, content, exported); sym != nil {
			syms = append(syms, sym)
		}

	case "type_alias_declaration":
		if sym := t.extractTypeAlias(path, node, content, exported); sym != nil {
			syms = append(syms, sym)
		}

	case "lexical_declaration":
		syms = append(syms, t.extractLexicalDecl(path, node, content, parent, exported)...)

	case "enum_declaration":
		if sym := t.extractEnum(path, node, content, exported); sym != nil {
			syms = append(syms, sym)
		}

	case "module":
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			name := nodeText(nameNode, content)
			syms = append(syms, &graph.Symbol{
				ID:         graph.NewSymbolID(path, name),
				Name:       name,
				Kind:       graph.KindModule,
				File:       path,
				Language:   graph.LangTypeScript,
				StartByte:  uint32(node.StartByte()),
				EndByte:    uint32(node.EndByte()),
				StartLine:  uint32(node.StartPosition().Row) + 1,
				EndLine:    uint32(node.EndPosition().Row) + 1,
				Exported:   exported,
				DocComment: prevSiblingComment(node, content),
			})
		}
	}

	return syms
}

func (t *TypeScriptExtractor) extractImports(root *tree_sitter.Node, content []byte) []graph.Import {
	var imports []graph.Import

	walkNodes(root, func(n *tree_sitter.Node) {
		if n.Kind() != "import_statement" {
			return
		}

		imp := graph.Import{
			Line: int(n.StartPosition().Row) + 1,
		}

		if src := n.ChildByFieldName("source"); src != nil {
			imp.Path = strings.Trim(nodeText(src, content), `"'`)
		}

		if imp.Path != "" {
			imports = append(imports, imp)
		}
	})

	return imports
}

func (t *TypeScriptExtractor) extractFunction(path string, node *tree_sitter.Node, content []byte, parent string, exported bool) *graph.Symbol {
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
		Language:   graph.LangTypeScript,
		StartByte:  uint32(node.StartByte()),
		EndByte:    uint32(node.EndByte()),
		StartLine:  uint32(node.StartPosition().Row) + 1,
		EndLine:    uint32(node.EndPosition().Row) + 1,
		Signature:  extractSignatureLine(node, content, "statement_block"),
		DocComment: prevSiblingComment(node, content),
		Exported:   exported,
		Parent:     parent,
	}
}

func (t *TypeScriptExtractor) extractClass(path string, node *tree_sitter.Node, content []byte, exported bool) []*graph.Symbol {
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
		Language:   graph.LangTypeScript,
		StartByte:  uint32(node.StartByte()),
		EndByte:    uint32(node.EndByte()),
		StartLine:  uint32(node.StartPosition().Row) + 1,
		EndLine:    uint32(node.EndPosition().Row) + 1,
		Signature:  extractSignatureLine(node, content, "class_body"),
		DocComment: prevSiblingComment(node, content),
		Exported:   exported,
	}
	syms = append(syms, classSym)

	body := node.ChildByFieldName("body")
	if body == nil {
		body = childByKind(node, "class_body")
	}
	if body != nil {
		for i := uint(0); i < body.ChildCount(); i++ {
			member := body.Child(i)
			if member != nil && member.Kind() == "method_definition" {
				if sym := t.extractClassMethod(path, member, content, className); sym != nil {
					syms = append(syms, sym)
				}
			}
		}
	}

	return syms
}

func (t *TypeScriptExtractor) extractClassMethod(path string, node *tree_sitter.Node, content []byte, className string) *graph.Symbol {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	name := nodeText(nameNode, content)
	qualName := className + "." + name

	return &graph.Symbol{
		ID:         graph.NewSymbolID(path, qualName),
		Name:       name,
		Kind:       graph.KindMethod,
		File:       path,
		Language:   graph.LangTypeScript,
		StartByte:  uint32(node.StartByte()),
		EndByte:    uint32(node.EndByte()),
		StartLine:  uint32(node.StartPosition().Row) + 1,
		EndLine:    uint32(node.EndPosition().Row) + 1,
		Signature:  extractSignatureLine(node, content, "statement_block"),
		DocComment: prevSiblingComment(node, content),
		Exported:   true,
		Parent:     className,
	}
}

func (t *TypeScriptExtractor) extractInterface(path string, node *tree_sitter.Node, content []byte, exported bool) *graph.Symbol {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	name := nodeText(nameNode, content)

	return &graph.Symbol{
		ID:         graph.NewSymbolID(path, name),
		Name:       name,
		Kind:       graph.KindInterface,
		File:       path,
		Language:   graph.LangTypeScript,
		StartByte:  uint32(node.StartByte()),
		EndByte:    uint32(node.EndByte()),
		StartLine:  uint32(node.StartPosition().Row) + 1,
		EndLine:    uint32(node.EndPosition().Row) + 1,
		Signature:  extractSignatureLine(node, content, "object_type"),
		DocComment: prevSiblingComment(node, content),
		Exported:   exported,
	}
}

func (t *TypeScriptExtractor) extractTypeAlias(path string, node *tree_sitter.Node, content []byte, exported bool) *graph.Symbol {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	name := nodeText(nameNode, content)

	sig := strings.TrimSpace(nodeText(node, content))
	if len(sig) > 200 {
		sig = sig[:200] + "..."
	}

	return &graph.Symbol{
		ID:         graph.NewSymbolID(path, name),
		Name:       name,
		Kind:       graph.KindType,
		File:       path,
		Language:   graph.LangTypeScript,
		StartByte:  uint32(node.StartByte()),
		EndByte:    uint32(node.EndByte()),
		StartLine:  uint32(node.StartPosition().Row) + 1,
		EndLine:    uint32(node.EndPosition().Row) + 1,
		Signature:  sig,
		DocComment: prevSiblingComment(node, content),
		Exported:   exported,
	}
}

func (t *TypeScriptExtractor) extractEnum(path string, node *tree_sitter.Node, content []byte, exported bool) *graph.Symbol {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	name := nodeText(nameNode, content)

	return &graph.Symbol{
		ID:         graph.NewSymbolID(path, name),
		Name:       name,
		Kind:       graph.KindType,
		File:       path,
		Language:   graph.LangTypeScript,
		StartByte:  uint32(node.StartByte()),
		EndByte:    uint32(node.EndByte()),
		StartLine:  uint32(node.StartPosition().Row) + 1,
		EndLine:    uint32(node.EndPosition().Row) + 1,
		Signature:  extractSignatureLine(node, content, "enum_body"),
		DocComment: prevSiblingComment(node, content),
		Exported:   exported,
	}
}

func (t *TypeScriptExtractor) extractLexicalDecl(path string, node *tree_sitter.Node, content []byte, parent string, exported bool) []*graph.Symbol {
	var syms []*graph.Symbol

	walkNodes(node, func(n *tree_sitter.Node) {
		if n.Kind() != "variable_declarator" {
			return
		}

		nameNode := n.ChildByFieldName("name")
		if nameNode == nil {
			return
		}
		name := nodeText(nameNode, content)

		value := n.ChildByFieldName("value")
		kind := graph.KindVar

		if value != nil {
			switch value.Kind() {
			case "arrow_function", "function":
				kind = graph.KindFunction
			}
		}

		if kind != graph.KindFunction && !isUpperSnakeCase(name) {
			return
		}

		qualName := name
		if parent != "" {
			qualName = parent + "." + name
		}

		sig := strings.TrimSpace(nodeText(node, content))
		if len(sig) > 200 {
			sig = sig[:200] + "..."
		}

		syms = append(syms, &graph.Symbol{
			ID:         graph.NewSymbolID(path, qualName),
			Name:       name,
			Kind:       kind,
			File:       path,
			Language:   graph.LangTypeScript,
			StartByte:  uint32(node.StartByte()),
			EndByte:    uint32(node.EndByte()),
			StartLine:  uint32(node.StartPosition().Row) + 1,
			EndLine:    uint32(node.EndPosition().Row) + 1,
			Signature:  sig,
			DocComment: prevSiblingComment(node, content),
			Exported:   exported,
			Parent:     parent,
		})
	})

	return syms
}

func tsModuleName(path string) string {
	name := path
	for _, suffix := range []string{".tsx", ".ts", ".jsx", ".js"} {
		name = strings.TrimSuffix(name, suffix)
	}
	name = strings.TrimSuffix(name, "/index")
	return name
}
