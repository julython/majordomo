package context

import (
	"fmt"
	"strings"
)

// Operation describes what the LLM should do with the target.
type Operation int

const (
	OpReplace Operation = iota // Replace the target symbol with new code
	OpAdd                      // Add a new symbol near the target
	OpExplain                  // Explain the target (no edit)
)

// PromptOptions controls how the prompt is rendered.
type PromptOptions struct {
	Operation   Operation
	Task        string // user's description of what to do
	OutputFence string // code fence language hint (go, python, typescript)
}

// RenderPrompt produces the LLM-ready prompt string from assembled context.
// This is the document the local model receives — curated, minimal, and precise.
func RenderPrompt(ctx *AssembledContext, opts PromptOptions) string {
	var b strings.Builder

	fence := opts.OutputFence
	if fence == "" {
		fence = ctx.Language.String()
	}

	// System framing — short, direct
	switch opts.Operation {
	case OpReplace:
		fmt.Fprintf(&b, "Replace the target %s below. ", ctx.Target.Symbol.Kind)
		fmt.Fprintf(&b, "Respond with ONLY the replacement code in a ```%s``` block. ", fence)
		b.WriteString("Keep the same function signature unless the task requires changing it. ")
		b.WriteString("Match the style of the surrounding code.\n\n")
	case OpAdd:
		fmt.Fprintf(&b, "Add new code to %s. ", ctx.FilePath)
		fmt.Fprintf(&b, "Respond with ONLY the new code in a ```%s``` block.\n\n", fence)
	case OpExplain:
		b.WriteString("Explain the following code. Be concise.\n\n")
	}

	// --- Section: Task ---
	if opts.Task != "" {
		b.WriteString("## Task\n")
		b.WriteString(opts.Task)
		b.WriteString("\n\n")
	}

	// --- Section: Target ---
	b.WriteString("## Target\n")
	fmt.Fprintf(&b, "# %s %s in %s (lines %d–%d)\n",
		ctx.Target.Symbol.Kind,
		ctx.Target.Symbol.Name,
		ctx.FilePath,
		ctx.Target.Symbol.StartLine,
		ctx.Target.Symbol.EndLine,
	)
	if ctx.Target.Symbol.DocComment != "" {
		fmt.Fprintf(&b, "# Doc: %s\n", firstLine(ctx.Target.Symbol.DocComment))
	}
	fmt.Fprintf(&b, "```%s\n%s\n```\n\n", fence, ctx.Target.Body)

	// --- Section: File imports ---
	if len(ctx.Imports) > 0 {
		b.WriteString("## Imports in this file\n")
		for _, imp := range ctx.Imports {
			b.WriteString(imp.Path)
			if imp.Alias != "" {
				fmt.Fprintf(&b, " (as %s)", imp.Alias)
			}
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}

	// --- Section: Dependencies (what target calls) ---
	if len(ctx.Dependencies) > 0 {
		b.WriteString("## Functions/types the target uses (signatures only, do NOT modify)\n")
		for _, dep := range ctx.Dependencies {
			fmt.Fprintf(&b, "%s  // %s:%d\n", dep.Signature, dep.File, dep.Line)
		}
		b.WriteByte('\n')
	}

	// --- Section: Dependents (who calls target) ---
	if len(ctx.Dependents) > 0 {
		b.WriteString("## Callers of this target (for context on expected behavior)\n")
		for _, dep := range ctx.Dependents {
			fmt.Fprintf(&b, "%s  // %s:%d\n", dep.Signature, dep.File, dep.Line)
		}
		b.WriteByte('\n')
	}

	// --- Section: Examples ---
	if len(ctx.Examples) > 0 {
		b.WriteString("## Similar patterns in this codebase (match this style)\n")
		for _, ex := range ctx.Examples {
			fmt.Fprintf(&b, "# %s in %s\n", ex.Symbol.Name, ex.Symbol.File)
			fmt.Fprintf(&b, "```%s\n%s\n```\n", fence, ex.Body)
		}
		b.WriteByte('\n')
	}

	// --- Section: Tests ---
	if len(ctx.Tests) > 0 {
		b.WriteString("## Related tests (your changes must keep these passing)\n")
		for _, t := range ctx.Tests {
			fmt.Fprintf(&b, "# %s in %s\n", t.Symbol.Name, t.Symbol.File)
			fmt.Fprintf(&b, "```%s\n%s\n```\n", fence, t.Body)
		}
		b.WriteByte('\n')
	}

	// --- Section: Siblings ---
	if len(ctx.Siblings) > 0 {
		b.WriteString("## Other symbols in the same file\n")
		for _, sib := range ctx.Siblings {
			b.WriteString(sib.Signature)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}

	// --- Truncation notice ---
	if ctx.Truncated {
		b.WriteString("# Note: context was trimmed to fit token budget. Some examples/tests omitted.\n")
	}

	return b.String()
}

// RenderHuman produces a human-readable version of the context
// (for the `repomap context` CLI command). Same content, friendlier formatting.
func RenderHuman(ctx *AssembledContext) string {
	var b strings.Builder

	fmt.Fprintf(&b, "=== Context for %s ===\n", ctx.Target.Symbol.ID)
	fmt.Fprintf(&b, "File: %s  Language: %s  Tokens: ~%d",
		ctx.FilePath, ctx.Language, ctx.TokenEstimate)
	if ctx.Truncated {
		b.WriteString(" (TRIMMED)")
	}
	b.WriteString("\n\n")

	// Target
	b.WriteString("── Target ──────────────────────────────────────\n")
	fmt.Fprintf(&b, "%s %s  lines %d–%d\n",
		ctx.Target.Symbol.Kind, ctx.Target.Symbol.Name,
		ctx.Target.Symbol.StartLine, ctx.Target.Symbol.EndLine)
	if ctx.Target.Symbol.DocComment != "" {
		fmt.Fprintf(&b, "  doc: %s\n", firstLine(ctx.Target.Symbol.DocComment))
	}
	b.WriteByte('\n')
	b.WriteString(ctx.Target.Body)
	b.WriteString("\n\n")

	// Imports
	if len(ctx.Imports) > 0 {
		b.WriteString("── Imports ─────────────────────────────────────\n")
		for _, imp := range ctx.Imports {
			fmt.Fprintf(&b, "  %s", imp.Path)
			if imp.Alias != "" {
				fmt.Fprintf(&b, " as %s", imp.Alias)
			}
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}

	// Dependencies
	if len(ctx.Dependencies) > 0 {
		fmt.Fprintf(&b, "── Calls (%d) ──────────────────────────────────\n", len(ctx.Dependencies))
		for _, dep := range ctx.Dependencies {
			fmt.Fprintf(&b, "  → %s  %s:%d\n", dep.Signature, dep.File, dep.Line)
		}
		b.WriteByte('\n')
	}

	// Dependents
	if len(ctx.Dependents) > 0 {
		fmt.Fprintf(&b, "── Called by (%d) ──────────────────────────────\n", len(ctx.Dependents))
		for _, dep := range ctx.Dependents {
			fmt.Fprintf(&b, "  ← %s  %s:%d\n", dep.Signature, dep.File, dep.Line)
		}
		b.WriteByte('\n')
	}

	// Examples
	if len(ctx.Examples) > 0 {
		fmt.Fprintf(&b, "── Examples (%d) ───────────────────────────────\n", len(ctx.Examples))
		for _, ex := range ctx.Examples {
			fmt.Fprintf(&b, "  # %s in %s (score: same-kind/package/name pattern)\n",
				ex.Symbol.Name, ex.Symbol.File)
			// Show first 5 lines of body as preview
			lines := strings.Split(ex.Body, "\n")
			limit := 5
			if len(lines) < limit {
				limit = len(lines)
			}
			for _, line := range lines[:limit] {
				fmt.Fprintf(&b, "  │ %s\n", line)
			}
			if len(lines) > 5 {
				fmt.Fprintf(&b, "  │ ... (%d more lines)\n", len(lines)-5)
			}
			b.WriteByte('\n')
		}
	}

	// Tests
	if len(ctx.Tests) > 0 {
		fmt.Fprintf(&b, "── Tests (%d) ──────────────────────────────────\n", len(ctx.Tests))
		for _, t := range ctx.Tests {
			fmt.Fprintf(&b, "  # %s in %s\n", t.Symbol.Name, t.Symbol.File)
			lines := strings.Split(t.Body, "\n")
			limit := 5
			if len(lines) < limit {
				limit = len(lines)
			}
			for _, line := range lines[:limit] {
				fmt.Fprintf(&b, "  │ %s\n", line)
			}
			if len(lines) > 5 {
				fmt.Fprintf(&b, "  │ ... (%d more lines)\n", len(lines)-5)
			}
			b.WriteByte('\n')
		}
	}

	// Siblings
	if len(ctx.Siblings) > 0 {
		fmt.Fprintf(&b, "── Siblings (%d) ───────────────────────────────\n", len(ctx.Siblings))
		for _, sib := range ctx.Siblings {
			fmt.Fprintf(&b, "  %s %s  :%d\n", sib.Kind, sib.Signature, sib.Line)
		}
		b.WriteByte('\n')
	}

	return b.String()
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx != -1 {
		return s[:idx]
	}
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}
