package analyze

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
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
		return json.NewEncoder(os.Stdout).Encode(Output{
			Summary: summary, Grade: report, Narrative: narrative,
		})
	}

	// Print the scorecard immediately
	printScorecard(sink, summary, report, data)

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
