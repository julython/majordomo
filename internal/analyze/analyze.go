package analyze

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/julython/majordomo/internal/grade"
	"github.com/julython/majordomo/internal/llm"
	"github.com/julython/majordomo/internal/mdrender"
)

// Sink is how analyze writes output.
type Sink interface {
	Print(text string)
	PrintMarkdown(text string)
	PrintStyled(line string)
	Status(text string)
	Error(text string)
	Finish(summary string)
}

type markdownRenderer interface {
	Render(src string) (string, error)
}

type stdoutSink struct {
	w      io.Writer
	md     markdownRenderer
	mdWrap int
}

func (s *stdoutSink) Print(text string)       { fmt.Fprintln(s.w, text) }
func (s *stdoutSink) PrintStyled(line string) { fmt.Fprintln(s.w, line) }
func (s *stdoutSink) Status(text string)      { fmt.Fprintln(os.Stderr, text) }
func (s *stdoutSink) Error(text string)       { fmt.Fprintln(os.Stderr, "error:", text) }
func (s *stdoutSink) Finish(summary string)   {}

func (s *stdoutSink) PrintMarkdown(text string) {
	if text == "" {
		fmt.Fprintln(s.w)
		return
	}
	wrap := mdrender.TermWidth(80) - 2
	if wrap < 20 {
		wrap = 20
	}
	if s.md == nil || s.mdWrap != wrap {
		r, err := mdrender.NewRenderer(wrap)
		if err != nil {
			fmt.Fprintln(s.w, text)
			return
		}
		s.md = r
		s.mdWrap = wrap
	}
	out, err := s.md.Render(text)
	if err != nil {
		fmt.Fprintln(s.w, text)
		return
	}
	fmt.Fprintln(s.w, strings.TrimRight(out, "\n"))
}

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

// Run is the original entrypoint — writes to stdout/stderr.
func Run(ctx context.Context, path string, client llm.Client, jsonOut bool) error {
	return RunWithSink(ctx, path, client, jsonOut, &stdoutSink{w: os.Stdout})
}

// RunWithSink runs analysis with all output going through the sink.
func RunWithSink(ctx context.Context, path string, client llm.Client, jsonOut bool, sink Sink) error {
	sink.Status("Scanning repository...")

	data, err := Collect(ctx, path)
	if err != nil {
		return fmt.Errorf("collecting repo data: %w", err)
	}
	if ctx.Err() != nil {
		return nil
	}

	sink.Status(fmt.Sprintf("Scanned %d files (%d source, %d tests)",
		len(data.Files), len(data.SourceFiles), len(data.TestFiles)))

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

	// JSON mode: need full narrative before encoding
	if jsonOut {
		var narrative string
		if client != nil {
			narrative, _ = client.Generate(ctx, BuildPrompt(data, report))
		}
		return json.NewEncoder(os.Stdout).Encode(Output{
			Summary: summary, Grade: report, Narrative: narrative,
		})
	}

	// Print the scorecard immediately
	printScorecard(sink, summary, report, data)

	// Stream narrative if LLM available
	if client != nil {
		sink.Status(fmt.Sprintf("Generating narrative with %s...", client.Name()))

		lead := RenderAssessmentLead(scorecardTerminalWidth())
		for _, line := range strings.Split(lead, "\n") {
			sink.PrintStyled(line)
		}
		sink.PrintStyled("")

		prompt := BuildPrompt(data, report)
		var lineBuf strings.Builder

		_, err = client.Stream(ctx, prompt, func(token string) {
			for _, ch := range token {
				if ch == '\n' {
					sink.PrintMarkdown(lineBuf.String())
					lineBuf.Reset()
				} else {
					lineBuf.WriteRune(ch)
				}
			}
		})

		// Flush last partial line
		if lineBuf.Len() > 0 {
			sink.PrintMarkdown(lineBuf.String())
		}

		if ctx.Err() == nil && err != nil {
			sink.Error(fmt.Sprintf("LLM: %v", err))
		}

		sink.Print("")
	}

	sink.Finish("")
	return nil
}

func scorecardTerminalWidth() int {
	w := mdrender.TermWidth(80) - 6
	if w < 52 {
		return 52
	}
	return w
}

func printScorecard(sink Sink, summary Summary, report *grade.Report, data *RepoData) {
	out := strings.TrimSuffix(RenderScorecard(summary, report, data, scorecardTerminalWidth()), "\n")
	for _, line := range strings.Split(out, "\n") {
		sink.PrintStyled(line)
	}
}

func BuildPrompt(data *RepoData, report *grade.Report) string {
	var b strings.Builder

	b.WriteString(`You are a senior engineering consultant grading a project's health and AI-readiness. Given this data, write a brief report card.

Be direct and specific. Roast what's bad, praise what's good. Reference actual numbers. End with the top 3 actions that would improve the score the most.

`)
	b.WriteString(fmt.Sprintf("Overall: %d%% (%s)\n\n", int(report.OverallPct), report.Letter))

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

	issues := collectIssues(data.FileAnalyses)
	if len(issues) > 0 {
		b.WriteString("## Notable File Issues\n")
		for _, issue := range issues {
			b.WriteString(fmt.Sprintf("  • %s: %s\n", issue.Path, strings.Join(issue.Issues, "; ")))
		}
		b.WriteString("\n")
	}

	var complexFiles []FileAnalysis
	for _, f := range data.FileAnalyses {
		if f.Complexity == "high" {
			complexFiles = append(complexFiles, f)
		}
	}
	if len(complexFiles) > 0 {
		b.WriteString("## High Complexity Files\n")
		for _, f := range complexFiles[:min(10, len(complexFiles))] {
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
		ReadmeSize:           0,
	}
}
