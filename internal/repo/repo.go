package repo

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type FileEntry struct {
	Path      string
	Extension string
	Lines     int
	Size      int64
}

type GrepMatch struct {
	File       string
	LineNumber int
	Line       string
}

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	"target": true, "__pycache__": true, ".venv": true,
	"dist": true, "build": true, ".next": true,
}

func WalkFiles(root string) ([]FileEntry, error) {
	var files []FileEntry
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		ext := strings.TrimPrefix(filepath.Ext(path), ".")
		lines := countLines(path)
		files = append(files, FileEntry{
			Path:      rel,
			Extension: ext,
			Lines:     lines,
			Size:      info.Size(),
		})
		return nil
	})
	return files, err
}

func FileExists(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}

func DirExists(root, name string) bool {
	info, err := os.Stat(filepath.Join(root, name))
	return err == nil && info.IsDir()
}

func HasAny(root string, names []string) bool {
	for _, n := range names {
		if FileExists(root, n) || DirExists(root, n) {
			return true
		}
	}
	return false
}

func ReadFile(root, name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func FileSize(root, name string) int64 {
	info, err := os.Stat(filepath.Join(root, name))
	if err != nil {
		return 0
	}
	return info.Size()
}

func Grep(root, pattern string, files []FileEntry) []GrepMatch {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	var matches []GrepMatch
	for _, f := range files {
		path := filepath.Join(root, f.Path)
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				matches = append(matches, GrepMatch{
					File:       f.Path,
					LineNumber: lineNum,
					Line:       line,
				})
			}
		}
		file.Close()
	}
	return matches
}

func GrepCount(root, pattern string, files []FileEntry) int {
	return len(Grep(root, pattern, files))
}

func GrepFilesForPatterns(root string, files []string, patterns []string) bool {
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			continue
		}
		content := string(data)
		for _, p := range patterns {
			if strings.Contains(content, p) {
				return true
			}
		}
	}
	return false
}

func ReadmeContains(root string, keywords []string) bool {
	content, err := ReadFile(root, "README.md")
	if err != nil {
		return false
	}
	lower := strings.ToLower(content)
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func Languages(files []FileEntry) map[string]int {
	langMap := map[string]string{
		"go": "Go", "rs": "Rust", "py": "Python",
		"ts": "TypeScript", "tsx": "TypeScript",
		"js": "JavaScript", "jsx": "JavaScript",
		"java": "Java", "rb": "Ruby", "c": "C", "h": "C",
		"cpp": "C++", "hpp": "C++", "cs": "C#",
		"swift": "Swift", "kt": "Kotlin",
	}
	langs := make(map[string]int)
	for _, f := range files {
		if lang, ok := langMap[f.Extension]; ok {
			langs[lang] += f.Lines
		}
	}
	return langs
}

func TestFiles(files []FileEntry) []string {
	var tests []string
	for _, f := range files {
		p := strings.ToLower(f.Path)
		if strings.Contains(p, "test") || strings.Contains(p, "spec") ||
			strings.HasSuffix(p, "_test.go") || strings.HasSuffix(p, "_test.rs") {
			tests = append(tests, f.Path)
		}
	}
	return tests
}

func SourceFiles(files []FileEntry) []string {
	codeExts := map[string]bool{
		"go": true, "rs": true, "py": true, "ts": true, "tsx": true,
		"js": true, "jsx": true, "java": true, "rb": true, "c": true,
		"cpp": true, "cs": true, "swift": true, "kt": true,
	}
	var sources []string
	for _, f := range files {
		if codeExts[f.Extension] {
			sources = append(sources, f.Path)
		}
	}
	return sources
}

func FilesOverLines(files []FileEntry, threshold int) []FileEntry {
	var big []FileEntry
	for _, f := range files {
		if f.Lines > threshold {
			big = append(big, f)
		}
	}
	return big
}

func CIConfigContains(root string, patterns []string) bool {
	ciPaths := []string{".github/workflows", ".circleci", ".gitlab-ci.yml"}
	for _, ci := range ciPaths {
		full := filepath.Join(root, ci)
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		if info.IsDir() {
			filepath.WalkDir(full, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return nil
				}
				content := string(data)
				for _, p := range patterns {
					if strings.Contains(content, p) {
						return fmt.Errorf("found")
					}
				}
				return nil
			})
		} else {
			data, err := os.ReadFile(full)
			if err != nil {
				continue
			}
			content := string(data)
			for _, p := range patterns {
				if strings.Contains(content, p) {
					return true
				}
			}
		}
	}
	return false
}

func DocCommentRatio(root string, files []FileEntry) float64 {
	var total, docs int
	for _, f := range files {
		path := filepath.Join(root, f.Path)
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			total++
			trimmed := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(trimmed, "///") ||
				strings.HasPrefix(trimmed, "//!") ||
				strings.HasPrefix(trimmed, "/**") ||
				strings.HasPrefix(trimmed, "\"\"\"") ||
				strings.HasPrefix(trimmed, "# ") {
				docs++
			}
		}
		file.Close()
	}
	if total == 0 {
		return 0
	}
	return float64(docs) / float64(total)
}

func countLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	n := 0
	for scanner.Scan() {
		n++
	}
	return n
}
