package analyze

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/julython/majordomo/internal/grade"
)

type langEntry struct {
	name  string
	lines int
}

// Palette — saturated colors that read well on dark and light terminals.
var (
	scPurple  = lipgloss.Color("#A78BFA")
	scViolet  = lipgloss.Color("#7C3AED")
	scCyan    = lipgloss.Color("#22D3EE")
	scTeal    = lipgloss.Color("#2DD4BF")
	scGreen   = lipgloss.Color("#4ADE80")
	scLime    = lipgloss.Color("#A3E635")
	scAmber   = lipgloss.Color("#FBBF24")
	scOrange  = lipgloss.Color("#FB923C")
	scRose    = lipgloss.Color("#FB7185")
	scRed     = lipgloss.Color("#F87171")
	scMuted   = lipgloss.Color("#94A3B8")
	scDim     = lipgloss.Color("#64748B")
	scWhite   = lipgloss.Color("#F1F5F9")
	scIndigo  = lipgloss.Color("#818CF8")
	scMagenta = lipgloss.Color("#E879F9")
)

// RenderScorecard returns a lipgloss-rendered report card (may include ANSI sequences).
func RenderScorecard(summary Summary, report *grade.Report, data *RepoData, maxWidth int) string {
	if maxWidth < 48 {
		maxWidth = 48
	}

	innerW := maxWidth - 4
	if innerW < 40 {
		innerW = 40
	}

	title := lipgloss.NewStyle().
		Foreground(scCyan).
		Bold(true).
		Render("⚡ majordomo")

	sub := lipgloss.NewStyle().
		Foreground(scMuted).
		Render(" — repository report card")

	headerLine := lipgloss.JoinHorizontal(lipgloss.Left, title, sub)

	pct := int(report.OverallPct)
	letter := report.Letter
	overallColors := letterGradeColors(letter)
	overallBig := lipgloss.NewStyle().
		Foreground(overallColors.fg).
		Background(overallColors.bg).
		Bold(true).
		Padding(0, 1).
		Render(fmt.Sprintf("%d%%", pct))
	letterStyled := lipgloss.NewStyle().
		Foreground(overallColors.fg).
		Bold(true).
		Render(fmt.Sprintf("(%s)", letter))
	overallRow := lipgloss.JoinHorizontal(lipgloss.Center,
		lipgloss.NewStyle().Foreground(scWhite).Bold(true).Render("Overall  "),
		overallBig,
		"  ",
		letterStyled,
	)

	stats := formatStatsBlock(summary, innerW)

	var catBlocks []string
	for _, cat := range report.Categories {
		catBlocks = append(catBlocks, formatCategory(cat, innerW))
	}
	catsJoined := strings.Join(catBlocks, "\n")

	var tail string
	var complexN int
	for _, f := range data.FileAnalyses {
		if f.Complexity == "high" {
			complexN++
		}
	}
	if complexN > 0 {
		tail = lipgloss.NewStyle().
			Foreground(scAmber).
			Bold(true).
			Render(fmt.Sprintf("⚠  %d high-complexity files", complexN))
	}

	bodyParts := []string{headerLine, "", overallRow, "", stats, "", catsJoined}
	if tail != "" {
		bodyParts = append(bodyParts, "", tail)
	}
	body := strings.Join(bodyParts, "\n")

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(scViolet).
		Padding(1, 1).
		Width(maxWidth).
		Foreground(scWhite)

	return box.Render(body)
}

// RenderAssessmentLead is printed before streamed LLM markdown (colored rule + title).
func RenderAssessmentLead(maxWidth int) string {
	if maxWidth < 24 {
		maxWidth = 48
	}
	ruleW := maxWidth - 4
	if ruleW > 56 {
		ruleW = 56
	}
	rule := lipgloss.NewStyle().
		Foreground(scIndigo).
		Render(strings.Repeat("·", ruleW))

	title := lipgloss.NewStyle().
		Foreground(scMagenta).
		Bold(true).
		Render("Narrative assessment")
	suffix := lipgloss.NewStyle().Foreground(scTeal).Render("(LLM)")
	centered := lipgloss.NewStyle().
		Width(maxWidth).
		Align(lipgloss.Center).
		Render(lipgloss.JoinHorizontal(lipgloss.Top, title, "  ", suffix))

	return "\n" + rule + "\n" + centered + "\n" + lipgloss.NewStyle().Foreground(scDim).Render(strings.Repeat("─", ruleW))
}

type gradeColors struct {
	fg lipgloss.Color
	bg lipgloss.Color
}

func letterGradeColors(letter string) gradeColors {
	switch {
	case strings.HasPrefix(letter, "A"):
		return gradeColors{scLime, lipgloss.Color("#14532D")}
	case strings.HasPrefix(letter, "B"):
		return gradeColors{scTeal, lipgloss.Color("#134E4A")}
	case strings.HasPrefix(letter, "C"):
		return gradeColors{scAmber, lipgloss.Color("#713F12")}
	case strings.HasPrefix(letter, "D"):
		return gradeColors{scOrange, lipgloss.Color("#7C2D12")}
	default:
		return gradeColors{scRose, lipgloss.Color("#881337")}
	}
}

func pctBarColors(pct float64) (fill, empty lipgloss.Color) {
	switch {
	case pct >= 80:
		return scGreen, lipgloss.Color("#064E3B")
	case pct >= 60:
		return scTeal, lipgloss.Color("#0F766E")
	case pct >= 40:
		return scAmber, lipgloss.Color("#78350F")
	default:
		return scRose, lipgloss.Color("#831843")
	}
}

func formatStatsBlock(summary Summary, innerW int) string {
	var langs []langEntry
	for k, v := range summary.Languages {
		langs = append(langs, langEntry{k, v})
	}
	sort.Slice(langs, func(i, j int) bool { return langs[i].lines > langs[j].lines })
	var langStrs []string
	for _, l := range langs {
		if len(langStrs) >= 6 {
			break
		}
		langStrs = append(langStrs,
			lipgloss.NewStyle().Foreground(scCyan).Render(l.name)+
				lipgloss.NewStyle().Foreground(scMuted).Render(fmt.Sprintf(" %d", l.lines)))
	}

	label := lipgloss.NewStyle().Foreground(scDim)
	val := lipgloss.NewStyle().Foreground(scWhite)
	sep := lipgloss.NewStyle().Foreground(scMuted).Render(" · ")

	line1 := lipgloss.JoinHorizontal(lipgloss.Left,
		label.Render("Files & size  "),
		lipgloss.NewStyle().Foreground(scPurple).Bold(true).Render(fmt.Sprintf("%d", summary.TotalFiles)),
		val.Render(" files"),
		sep,
		lipgloss.NewStyle().Foreground(scIndigo).Bold(true).Render(formatInt(summary.TotalLines)),
		val.Render(" lines"),
	)

	var langLine string
	if len(langStrs) == 0 {
		langLine = lipgloss.NewStyle().Foreground(scDim).Italic(true).Render("—")
	} else {
		var langJoined strings.Builder
		langSep := lipgloss.NewStyle().Foreground(scDim).Render(" · ")
		for i, s := range langStrs {
			if i > 0 {
				langJoined.WriteString(langSep)
			}
			langJoined.WriteString(s)
		}
		langLine = langJoined.String()
	}
	line2 := lipgloss.JoinHorizontal(lipgloss.Left,
		label.Render("Languages     "),
		langLine,
	)

	line3 := lipgloss.JoinHorizontal(lipgloss.Left,
		label.Render("Last 30 days  "),
		lipgloss.NewStyle().Foreground(scTeal).Bold(true).Render(fmt.Sprintf("%d", summary.Commits30d)),
		val.Render(" commits"),
		sep,
		lipgloss.NewStyle().Foreground(scMagenta).Bold(true).Render(fmt.Sprintf("%d", summary.Authors30d)),
		val.Render(" authors"),
	)

	line4 := lipgloss.JoinHorizontal(lipgloss.Left,
		label.Render("TODOs/FIXMEs  "),
		lipgloss.NewStyle().Foreground(scAmber).Render(fmt.Sprintf("%d", summary.TODOs)),
	)

	_ = innerW
	return strings.Join([]string{line1, line2, line3, line4}, "\n")
}

func formatInt(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%d,%03d", n/1000, n%1000)
	}
	return fmt.Sprintf("%d,%03d,%03d", n/1_000_000, (n/1000)%1000, n%1000)
}

func formatCategory(cat grade.CategoryGrade, innerW int) string {
	fillC, emptyC := pctBarColors(cat.Pct)
	seg := 14
	filled := int(cat.Pct / 100 * float64(seg))
	if filled > seg {
		filled = seg
	}
	if filled < 0 {
		filled = 0
	}
	bar := lipgloss.NewStyle().Foreground(fillC).Render(strings.Repeat("█", filled)) +
		lipgloss.NewStyle().Foreground(emptyC).Render(strings.Repeat("░", seg-filled))

	name := lipgloss.NewStyle().
		Foreground(scPurple).
		Bold(true).
		Width(22).
		Align(lipgloss.Left).
		Render(strings.ToUpper(cat.Name))

	pctStr := lipgloss.NewStyle().
		Foreground(fillC).
		Bold(true).
		Width(5).
		Align(lipgloss.Right).
		Render(fmt.Sprintf("%.0f%%", cat.Pct))

	top := lipgloss.JoinHorizontal(lipgloss.Center, name, " ", bar, " ", pctStr)

	var sigLines []string
	for _, s := range cat.Signals {
		var icon string
		var iconStyle lipgloss.Style
		if s.Passed {
			icon = "✓"
			iconStyle = lipgloss.NewStyle().Foreground(scGreen).Bold(true)
		} else {
			icon = "✗"
			iconStyle = lipgloss.NewStyle().Foreground(scRed).Bold(true)
		}
		nm := lipgloss.NewStyle().Foreground(scWhite).Bold(true).Render(s.Name)
		detail := lipgloss.NewStyle().Foreground(scMuted).Render(" — " + s.Detail)
		sigLines = append(sigLines, lipgloss.JoinHorizontal(lipgloss.Left, "    ", iconStyle.Render(icon), " ", nm, detail))
	}

	_ = innerW
	return top + "\n" + strings.Join(sigLines, "\n")
}
