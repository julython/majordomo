package analyze

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/julython/majordomo/internal/repo"
	"golang.org/x/sync/errgroup"
)

// RepoData holds everything we collect in parallel before sending to LLM.
type RepoData struct {
	mu sync.Mutex

	// File system
	Files       []repo.FileEntry
	Languages   map[string]int
	TestFiles   []string
	SourceFiles []string
	BigFiles    []repo.FileEntry
	TotalLines  int

	// Git
	Commits             []CommitInfo
	RecentCommits       []CommitInfo // last 30 days
	UniqueAuthors       int
	DaysSinceLastCommit int
	DirectPushes        int

	// Code quality
	TODOs    int
	FIXMEs   int
	HACKs    int
	DocRatio float64

	// Structure checks (all run concurrently)
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

	// Dependency
	DependencyCount int

	// Per-file analysis (expensive, fanned out)
	FileAnalyses []FileAnalysis
}

type CommitInfo struct {
	ID           string
	Author       string
	Message      string
	Timestamp    time.Time
	FilesChanged int
	IsDirectPush bool
}

type FileAnalysis struct {
	Path       string
	Language   string
	Lines      int
	Functions  int
	Complexity string // "low", "medium", "high"
	TODOs      []string
	Issues     []string
}

// Collect runs all analysis goroutines and returns the assembled data.
func Collect(ctx context.Context, root string) (*RepoData, error) {
	data := &RepoData{}
	g, ctx := errgroup.WithContext(ctx)

	// Phase 1: File walk (everything else depends on this)
	var files []repo.FileEntry
	g.Go(func() error {
		var err error
		files, err = repo.WalkFiles(root)
		if err != nil {
			return err
		}
		data.mu.Lock()
		data.Files = files
		data.Languages = repo.Languages(files)
		data.TestFiles = repo.TestFiles(files)
		data.SourceFiles = repo.SourceFiles(files)
		data.BigFiles = repo.FilesOverLines(files, 1000)
		for _, f := range files {
			data.TotalLines += f.Lines
		}
		data.mu.Unlock()
		return nil
	})

	// Wait for file walk before fanning out file-dependent work
	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Phase 2: Fan out everything that can run concurrently
	g, ctx = errgroup.WithContext(ctx)

	// Git history
	g.Go(func() error {
		commits := gitLog(root, 200)
		thirtyDaysAgo := time.Now().AddDate(0, 0, -30)
		var recent []CommitInfo
		authors := make(map[string]bool)
		directPushes := 0
		conventional := 0

		for _, c := range commits {
			if c.Timestamp.After(thirtyDaysAgo) {
				recent = append(recent, c)
			}
			authors[c.Author] = true
			if c.IsDirectPush {
				directPushes++
			}
			if isConventionalCommit(c.Message) {
				conventional++
			}
		}

		pct := 0
		if len(commits) > 0 {
			pct = conventional * 100 / len(commits)
		}

		daysSince := 999
		if len(commits) > 0 {
			daysSince = int(time.Since(commits[0].Timestamp).Hours() / 24)
		}

		data.mu.Lock()
		data.Commits = commits
		data.RecentCommits = recent
		data.UniqueAuthors = len(authors)
		data.DirectPushes = directPushes
		data.DaysSinceLastCommit = daysSince
		data.ConventionalPct = pct
		data.mu.Unlock()
		return nil
	})

	// Grep TODOs/FIXMEs/HACKs
	g.Go(func() error {
		todos := repo.GrepCount(root, `TODO`, files)
		fixmes := repo.GrepCount(root, `FIXME`, files)
		hacks := repo.GrepCount(root, `HACK|XXX`, files)
		data.mu.Lock()
		data.TODOs = todos
		data.FIXMEs = fixmes
		data.HACKs = hacks
		data.mu.Unlock()
		return nil
	})

	// Doc comment ratio
	g.Go(func() error {
		ratio := repo.DocCommentRatio(root, files)
		data.mu.Lock()
		data.DocRatio = ratio
		data.mu.Unlock()
		return nil
	})

	// Structure checks — each is cheap, fan them all out
	g.Go(func() error {
		data.mu.Lock()
		data.HasCI = repo.HasAny(root, []string{".github/workflows", ".circleci", ".gitlab-ci.yml", "Jenkinsfile"})
		data.mu.Unlock()
		return nil
	})
	g.Go(func() error {
		data.mu.Lock()
		data.HasLinter = repo.HasAny(root, []string{".eslintrc", ".eslintrc.json", "ruff.toml", ".golangci.yml", "clippy.toml", "biome.json"})
		data.mu.Unlock()
		return nil
	})
	g.Go(func() error {
		data.mu.Lock()
		data.HasFormatter = repo.HasAny(root, []string{".prettierrc", "rustfmt.toml", ".editorconfig", "biome.json"})
		data.mu.Unlock()
		return nil
	})
	g.Go(func() error {
		data.mu.Lock()
		data.HasPreCommit = repo.HasAny(root, []string{".pre-commit-config.yaml", ".husky"})
		data.mu.Unlock()
		return nil
	})
	g.Go(func() error {
		data.mu.Lock()
		data.HasLockfile = repo.HasAny(root, []string{
			"Cargo.lock", "package-lock.json", "yarn.lock", "pnpm-lock.yaml",
			"poetry.lock", "go.sum", "conda-lock.yml", "uv.lock",
		})
		data.mu.Unlock()
		return nil
	})
	g.Go(func() error {
		data.mu.Lock()
		data.HasSecurityScan = repo.HasAny(root, []string{".github/dependabot.yml", ".github/workflows/codeql.yml"})
		data.mu.Unlock()
		return nil
	})
	g.Go(func() error {
		data.mu.Lock()
		data.HasContributing = repo.FileExists(root, "CONTRIBUTING.md")
		data.HasPRTemplate = repo.FileExists(root, ".github/pull_request_template.md")
		data.HasIssueTemplates = repo.DirExists(root, ".github/ISSUE_TEMPLATE")
		data.HasCodeowners = repo.HasAny(root, []string{".github/CODEOWNERS", "CODEOWNERS"})
		data.HasAIContext = repo.HasAny(root, []string{".cursorrules", "CLAUDE.md", ".claude/settings.json", ".github/copilot-instructions.md"})
		data.HasArchDoc = repo.HasAny(root, []string{"ARCHITECTURE.md", "docs/architecture.md", "docs/design.md"})
		data.HasAPISpec = repo.HasAny(root, []string{"openapi.yaml", "openapi.json", "swagger.yaml"})
		data.HasADRs = repo.HasAny(root, []string{"docs/adr", "docs/decisions", "docs/rfcs"})
		data.HasChangelog = repo.FileExists(root, "CHANGELOG.md")
		data.HasSetupInstructions = repo.ReadmeContains(root, []string{"install", "setup", "getting started", "quickstart"})
		data.mu.Unlock()
		return nil
	})

	// Integration test detection
	g.Go(func() error {
		has := repo.GrepFilesForPatterns(root, data.TestFiles, []string{
			"testcontainers", "docker", "httptest", "TestClient",
			"supertest", "playwright", "cypress", "selenium",
		})
		data.mu.Lock()
		data.HasIntegrationTests = has
		data.mu.Unlock()
		return nil
	})

	// CI runs tests
	g.Go(func() error {
		runs := repo.CIConfigContains(root, []string{
			"test", "pytest", "jest", "cargo test", "go test",
			"npm test", "yarn test", "vitest",
		})
		data.mu.Lock()
		data.TestsInCI = runs
		data.mu.Unlock()
		return nil
	})

	// Per-file deep analysis — fan out across source files with bounded concurrency
	g.Go(func() error {
		analyses := analyzeFiles(ctx, root, data.SourceFiles)
		data.mu.Lock()
		data.FileAnalyses = analyses
		data.mu.Unlock()
		return nil
	})

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return data, nil
}

// analyzeFiles fans out per-file analysis with bounded concurrency.
func analyzeFiles(ctx context.Context, root string, files []string) []FileAnalysis {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(8) // max 8 concurrent file reads

	var mu sync.Mutex
	var results []FileAnalysis

	for _, f := range files {
		f := f
		g.Go(func() error {
			a := analyzeFile(root, f)
			mu.Lock()
			results = append(results, a)
			mu.Unlock()
			return nil
		})
	}
	g.Wait()
	return results
}

func analyzeFile(root, path string) FileAnalysis {
	content, err := repo.ReadFile(root, path)
	if err != nil {
		return FileAnalysis{Path: path}
	}

	lines := strings.Count(content, "\n")
	ext := strings.TrimPrefix(strings.ToLower(path[strings.LastIndex(path, "."):]), ".")

	// Count functions (simple heuristic per language)
	funcs := countFunctions(content, ext)

	// Complexity heuristic
	complexity := "low"
	if lines > 500 || funcs > 30 {
		complexity = "high"
	} else if lines > 200 || funcs > 15 {
		complexity = "medium"
	}

	// Collect TODOs from this file
	var todos []string
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "TODO") || strings.Contains(line, "FIXME") {
			todos = append(todos, strings.TrimSpace(line))
		}
	}

	// Flag issues
	var issues []string
	if lines > 1000 {
		issues = append(issues, "file exceeds 1000 lines")
	}
	if funcs > 30 {
		issues = append(issues, "too many functions — consider splitting")
	}
	if strings.Contains(content, "nolint") || strings.Contains(content, "noqa") || strings.Contains(content, "nosec") {
		issues = append(issues, "contains lint suppression directives")
	}

	return FileAnalysis{
		Path:       path,
		Language:   ext,
		Lines:      lines,
		Functions:  funcs,
		Complexity: complexity,
		TODOs:      todos,
		Issues:     issues,
	}
}

func countFunctions(content, ext string) int {
	var patterns []string
	switch ext {
	case "go":
		patterns = []string{"func "}
	case "py":
		patterns = []string{"def ", "async def "}
	case "rs":
		patterns = []string{"fn "}
	case "ts", "tsx", "js", "jsx":
		patterns = []string{"function ", "=> {", "=> ("}
	case "java", "kt":
		patterns = []string{"public ", "private ", "protected ", "fun "}
	default:
		return 0
	}

	count := 0
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		for _, p := range patterns {
			if strings.HasPrefix(trimmed, p) || strings.Contains(trimmed, " "+p) {
				count++
				break
			}
		}
	}
	return count
}

func gitLog(root string, count int) []CommitInfo {
	out, err := exec.Command("git", "-C", root, "log",
		"--pretty=format:%H|%an|%s|%aI",
		"--no-merges",
		"-n", fmt.Sprintf("%d", count),
	).Output()
	if err != nil {
		slog.Warn("git log failed", "error", err)
		return nil
	}

	var commits []CommitInfo
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}
		ts, _ := time.Parse(time.RFC3339, parts[3])
		commits = append(commits, CommitInfo{
			ID:        parts[0],
			Author:    parts[1],
			Message:   parts[2],
			Timestamp: ts,
		})
	}
	return commits
}

var conventionalRe = regexp.MustCompile(`^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\(.+\))?!?:`)

func isConventionalCommit(msg string) bool {
	return conventionalRe.MatchString(strings.TrimSpace(msg))
}
