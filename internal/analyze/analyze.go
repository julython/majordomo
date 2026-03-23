package analyze

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/julython/majordomo/internal/grade"
	"github.com/julython/majordomo/internal/llm"
)

type Output struct {
	Summary   Summary        `json:"summary"`
	Grade     *grade.Report  `json:"grade"`
	Files     []FileAnalysis `json:"files,omitempty"`
	Narrative string         `json:"narrative,omitempty"`
}

type Summary struct {
	Languages   map[string]int `json:"languages"`
	TotalFiles  int            `json:"total_files"`
	TotalLines  int            `json:"total_lines"`
	TestFiles   int            `json:"test_files"`
	SourceFiles int            `json:"source_files"`
	TODOs       int            `json:"todos"`
	Commits30d  int            `json:"commits_30d"`
	Authors30d  int            `json:"authors_30d"`
}

func Run(ctx context.Context, path string, client llm.Client, jsonOut bool) error {
	fmt.Fprintln(os.Stderr, "Scanning repository...")

	data, err := Collect(ctx, path)
	if err != nil {
		return fmt.Errorf("collecting repo data: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Scanned %d files (%d source, %d tests) in %d goroutines\n",
		len(data.Files), len(data.SourceFiles), len(data.TestFiles), 8)

	report := grade.FromData(data.ToGradeInput())

	summary := Summary{
		Languages:   data.Languages,
		TotalFiles:  len(data.Files),
		TotalLines:  data.TotalLines,
		TestFiles:   len(data.TestFiles),
		SourceFiles: len(data.SourceFiles),
		TODOs:       data.TODOs + data.FIXMEs,
		Commits30d:  len(data.RecentCommits),
		Authors30d:  data.UniqueAuthors,
	}

	var narrative string
	if client != nil {
		fmt.Fprintf(os.Stderr, "Generating narrative with %s...\n", client.Name())
		prompt := BuildPrompt(data, report)
		narrative, err = client.Generate(ctx, prompt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "LLM error (continuing without narrative): %v\n", err)
		}
	}

	output := Output{
		Summary:   summary,
		Grade:     report,
		Narrative: narrative,
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	printReport(summary, report, data, narrative)
	return nil
}

func BuildPrompt(data *RepoData, report *grade.Report) string {
	var b strings.Builder

	b.WriteString(`You are a senior engineering consultant grading a project's health and AI-readiness. Given this data, write a brief report card.

Be direct and specific. Roast what's bad, praise what's good. Reference actual numbers. End with the top 3 actions that would improve the score the most.

`)
	b.WriteString(fmt.Sprintf("Overall: %d%% (%s)\n\n", int(report.OverallPct), report.Letter))

	// Categories
	for _, cat := range report.Categories {
		b.WriteString(fmt.Sprintf("## %s (%.0f%%)\n", cat.Name, cat.Pct))
		for _, s := range cat.Signals {
			icon := "✓"
			if !s.Passed {
				icon = "✗"
			}
			b.WriteString(fmt.Sprintf("  %s %s: %s\n", icon, s.Name, s.Detail))
		}
		b.WriteString("\n")
	}

	// File-level issues (most interesting ones)
	issues := collectIssues(data.FileAnalyses)
	if len(issues) > 0 {
		b.WriteString("## Notable File Issues\n")
		for _, issue := range issues {
			b.WriteString(fmt.Sprintf("  • %s: %s\n", issue.Path, strings.Join(issue.Issues, "; ")))
		}
		b.WriteString("\n")
	}

	// High complexity files
	var complex []FileAnalysis
	for _, f := range data.FileAnalyses {
		if f.Complexity == "high" {
			complex = append(complex, f)
		}
	}
	if len(complex) > 0 {
		b.WriteString("## High Complexity Files\n")
		for _, f := range complex[:min(10, len(complex))] {
			b.WriteString(fmt.Sprintf("  • %s (%d lines, %d functions)\n", f.Path, f.Lines, f.Functions))
		}
		b.WriteString("\n")
	}

	return b.String()
}

func collectIssues(analyses []FileAnalysis) []FileAnalysis {
	var withIssues []FileAnalysis
	for _, a := range analyses {
		if len(a.Issues) > 0 {
			withIssues = append(withIssues, a)
		}
	}
	sort.Slice(withIssues, func(i, j int) bool {
		return len(withIssues[i].Issues) > len(withIssues[j].Issues)
	})
	if len(withIssues) > 15 {
		withIssues = withIssues[:15]
	}
	return withIssues
}

func printReport(summary Summary, report *grade.Report, data *RepoData, narrative string) {
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════╗")
	fmt.Println("║  MAJORDOMO REPORT CARD                      ║")
	fmt.Printf("║  Overall: %3d%% (%s)%-*s║\n", int(report.OverallPct), report.Letter, 37-len(report.Letter), "")
	fmt.Println("╚══════════════════════════════════════════════╝")
	fmt.Println()

	// Languages
	type langCount struct {
		name  string
		lines int
	}
	var langs []langCount
	for k, v := range summary.Languages {
		langs = append(langs, langCount{k, v})
	}
	sort.Slice(langs, func(i, j int) bool { return langs[i].lines > langs[j].lines })
	var langStrs []string
	for _, l := range langs {
		if len(langStrs) >= 5 {
			break
		}
		langStrs = append(langStrs, fmt.Sprintf("%s: %d", l.name, l.lines))
	}
	fmt.Printf("  %d files, %d lines\n", summary.TotalFiles, summary.TotalLines)
	fmt.Printf("  Languages: %s\n", strings.Join(langStrs, ", "))
	fmt.Printf("  %d commits by %d authors in last 30 days\n", summary.Commits30d, summary.Authors30d)
	fmt.Printf("  %d TODOs/FIXMEs\n", summary.TODOs)
	fmt.Println()

	// Categories
	for _, cat := range report.Categories {
		filled := int(cat.Pct / 10)
		empty := 10 - filled
		bar := strings.Repeat("█", filled) + strings.Repeat("░", empty)
		fmt.Printf("  %s [%s] %.0f%%\n", cat.Name, bar, cat.Pct)
		for _, s := range cat.Signals {
			icon := "  ✓"
			if !s.Passed {
				icon = "  ✗"
			}
			fmt.Printf("    %s %s — %s\n", icon, s.Name, s.Detail)
		}
		fmt.Println()
	}

	// File hotspots
	var complex int
	for _, f := range data.FileAnalyses {
		if f.Complexity == "high" {
			complex++
		}
	}
	if complex > 0 {
		fmt.Printf("  ⚠ %d high-complexity files detected\n\n", complex)
	}

	if narrative != "" {
		fmt.Println("─── Assessment ───────────────────────────────")
		fmt.Println()
		fmt.Println(narrative)
		fmt.Println()
	}
}

// ToGradeInput converts collected data to the grade package's input format.
func (d *RepoData) ToGradeInput() *grade.Input {
	return &grade.Input{
		HasCI:                d.HasCI,
		HasLinter:            d.HasLinter,
		HasFormatter:         d.HasFormatter,
		HasPreCommit:         d.HasPreCommit,
		HasLockfile:          d.HasLockfile,
		HasSecurityScan:      d.HasSecurityScan,
		HasContributing:      d.HasContributing,
		HasPRTemplate:        d.HasPRTemplate,
		HasIssueTemplates:    d.HasIssueTemplates,
		HasCodeowners:        d.HasCodeowners,
		HasAIContext:         d.HasAIContext,
		HasArchDoc:           d.HasArchDoc,
		HasAPISpec:           d.HasAPISpec,
		HasADRs:              d.HasADRs,
		HasChangelog:         d.HasChangelog,
		HasSetupInstructions: d.HasSetupInstructions,
		HasIntegrationTests:  d.HasIntegrationTests,
		TestsInCI:            d.TestsInCI,
		ConventionalPct:      d.ConventionalPct,
		TestFileCount:        len(d.TestFiles),
		SourceFileCount:      len(d.SourceFiles),
		TODOCount:            d.TODOs + d.FIXMEs + d.HACKs,
		BigFileCount:         len(d.BigFiles),
		DependencyCount:      d.DependencyCount,
		DaysSinceCommit:      d.DaysSinceLastCommit,
		DirectPushes:         d.DirectPushes,
		UniqueAuthors:        d.UniqueAuthors,
		DocRatio:             d.DocRatio,
		ReadmeSize:           0, // filled separately if needed
	}
}
