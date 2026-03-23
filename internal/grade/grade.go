package grade

import (
	"context"
	"fmt"
)

type Report struct {
	OverallPct float64         `json:"overall_pct"`
	Letter     string          `json:"letter"`
	Categories []CategoryGrade `json:"categories"`
}

type CategoryGrade struct {
	Name     string   `json:"name"`
	Score    int      `json:"score"`
	MaxScore int      `json:"max_score"`
	Pct      float64  `json:"pct"`
	Signals  []Signal `json:"signals"`
}

type Signal struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

// Input is the data contract between the collector and the grader.
// No file system access here — everything is pre-computed.
type Input struct {
	HasCI                bool
	HasLinter            bool
	HasFormatter         bool
	HasPreCommit         bool
	HasLockfile          bool
	HasSecurityScan      bool
	HasContributing      bool
	HasPRTemplate        bool
	HasIssueTemplates    bool
	HasCodeowners        bool
	HasAIContext         bool
	HasArchDoc           bool
	HasAPISpec           bool
	HasADRs              bool
	HasChangelog         bool
	HasSetupInstructions bool
	HasIntegrationTests  bool
	TestsInCI            bool
	ConventionalPct      int
	TestFileCount        int
	SourceFileCount      int
	TODOCount            int
	BigFileCount         int
	DependencyCount      int
	DaysSinceCommit      int
	DirectPushes         int
	UniqueAuthors        int
	DocRatio             float64
	ReadmeSize           int64
}

func sig(name string, passed bool, detail string) Signal {
	return Signal{Name: name, Passed: passed, Detail: detail}
}

func category(name string, signals []Signal) CategoryGrade {
	max := len(signals)
	score := 0
	for _, s := range signals {
		if s.Passed {
			score++
		}
	}
	pct := 0.0
	if max > 0 {
		pct = float64(score) / float64(max) * 100
	}
	return CategoryGrade{Name: name, Score: score, MaxScore: max, Pct: pct, Signals: signals}
}

func FromData(in *Input) *Report {
	cats := []CategoryGrade{
		aiReadiness(in),
		guardrails(in),
		testQuality(in),
		documentation(in),
		contributionHygiene(in),
		maintainability(in),
	}

	totalScore, totalMax := 0, 0
	for _, c := range cats {
		totalScore += c.Score
		totalMax += c.MaxScore
	}
	pct := 0.0
	if totalMax > 0 {
		pct = float64(totalScore) / float64(totalMax) * 100
	}

	letter := "F"
	switch {
	case pct >= 90:
		letter = "A"
	case pct >= 80:
		letter = "B+"
	case pct >= 70:
		letter = "B"
	case pct >= 60:
		letter = "C+"
	case pct >= 50:
		letter = "C"
	case pct >= 40:
		letter = "D"
	}

	return &Report{OverallPct: pct, Letter: letter, Categories: cats}
}

// Repo runs the full pipeline: collect + grade. Used by the CLI grade command.
func Repo(ctx context.Context, path string) (*Report, error) {
	// Import cycle prevention: grade doesn't call analyze.Collect directly.
	// The caller (main.go) should call analyze.Collect then grade.FromData.
	// This function is a convenience that does a minimal check.
	return nil, fmt.Errorf("use analyze.Collect + grade.FromData instead")
}

func PrintReport(r *Report) {
	// Delegated to analyze.printReport for now
	fmt.Printf("Overall: %d%% (%s)\n", int(r.OverallPct), r.Letter)
	for _, c := range r.Categories {
		fmt.Printf("  %s: %.0f%%\n", c.Name, c.Pct)
	}
}

func aiReadiness(in *Input) CategoryGrade {
	return category("AI Readiness", []Signal{
		sig("CONTRIBUTING.md", in.HasContributing, boolDetail(in.HasContributing, "found", "missing")),
		sig("PR template", in.HasPRTemplate, boolDetail(in.HasPRTemplate, "found", "missing — AI agents won't know your PR expectations")),
		sig("Issue templates", in.HasIssueTemplates, boolDetail(in.HasIssueTemplates, "found", "missing")),
		sig("CODEOWNERS", in.HasCodeowners, boolDetail(in.HasCodeowners, "found", "missing — PRs have no automatic reviewer assignment")),
		sig("AI context files", in.HasAIContext, boolDetail(in.HasAIContext, "found", "no .cursorrules, CLAUDE.md, or copilot instructions")),
		sig("Architecture doc", in.HasArchDoc, boolDetail(in.HasArchDoc, "found", "missing — no high-level guide for contributors")),
		sig("Conventional commits", in.ConventionalPct > 70, fmt.Sprintf("%d%% of recent commits", in.ConventionalPct)),
	})
}

func guardrails(in *Input) CategoryGrade {
	return category("Guardrails", []Signal{
		sig("CI configured", in.HasCI, boolDetail(in.HasCI, "found", "no CI detected")),
		sig("Linter configured", in.HasLinter, boolDetail(in.HasLinter, "found", "no linter config detected")),
		sig("Formatter configured", in.HasFormatter, boolDetail(in.HasFormatter, "found", "no formatter — inconsistent style")),
		sig("Pre-commit hooks", in.HasPreCommit, boolDetail(in.HasPreCommit, "found", "none — bad code can be committed unchecked")),
		sig("Dependency lockfile", in.HasLockfile, boolDetail(in.HasLockfile, "found", "missing — builds not reproducible")),
		sig("Security scanning", in.HasSecurityScan, boolDetail(in.HasSecurityScan, "found", "no dependabot or codeql")),
	})
}

func testQuality(in *Input) CategoryGrade {
	ratio := 0.0
	if in.SourceFileCount > 0 {
		ratio = float64(in.TestFileCount) / float64(in.SourceFileCount)
	}
	return category("Test Quality", []Signal{
		sig("Tests exist", in.TestFileCount > 0, fmt.Sprintf("%d test files", in.TestFileCount)),
		sig("Test ratio > 20%", ratio > 0.2, fmt.Sprintf("%.0f%% (%d test / %d source)", ratio*100, in.TestFileCount, in.SourceFileCount)),
		sig("Integration tests", in.HasIntegrationTests, boolDetail(in.HasIntegrationTests, "found", "none detected")),
		sig("Tests run in CI", in.TestsInCI, boolDetail(in.TestsInCI, "CI runs tests", "tests not found in CI config")),
	})
}

func documentation(in *Input) CategoryGrade {
	return category("Documentation", []Signal{
		sig("README is substantial", in.ReadmeSize > 500, fmt.Sprintf("%d bytes", in.ReadmeSize)),
		sig("API docs or OpenAPI spec", in.HasAPISpec, boolDetail(in.HasAPISpec, "found", "no API spec")),
		sig("Inline doc coverage", in.DocRatio > 0.05, fmt.Sprintf("%.1f%%", in.DocRatio*100)),
		sig("ADRs or decision records", in.HasADRs, boolDetail(in.HasADRs, "found", "no decision records")),
		sig("Changelog", in.HasChangelog, boolDetail(in.HasChangelog, "found", "missing")),
		sig("Setup instructions", in.HasSetupInstructions, boolDetail(in.HasSetupInstructions, "README has setup section", "missing install/setup section")),
	})
}

func contributionHygiene(in *Input) CategoryGrade {
	return category("Contribution Hygiene", []Signal{
		sig("Limited direct pushes", in.DirectPushes < 5, fmt.Sprintf("%d in recent commits", in.DirectPushes)),
		sig("Bus factor > 1", in.UniqueAuthors > 1, fmt.Sprintf("%d unique authors", in.UniqueAuthors)),
		sig("Active recently", in.DaysSinceCommit < 30, fmt.Sprintf("%d days since last commit", in.DaysSinceCommit)),
	})
}

func maintainability(in *Input) CategoryGrade {
	return category("Maintainability", []Signal{
		sig("Low TODO/FIXME count", in.TODOCount < 50, fmt.Sprintf("%d found", in.TODOCount)),
		sig("No giant files (>1000 lines)", in.BigFileCount < 5, fmt.Sprintf("%d files over 1000 lines", in.BigFileCount)),
		sig("Dependencies not bloated", in.DependencyCount < 100, fmt.Sprintf("%d direct dependencies", in.DependencyCount)),
		sig("Recent activity", in.DaysSinceCommit < 30, fmt.Sprintf("%d days since last commit", in.DaysSinceCommit)),
	})
}

func boolDetail(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}
