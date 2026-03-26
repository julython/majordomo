package tui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"

	"github.com/julython/majordomo/internal/mdrender"
)

// StreamSink writes command output directly to the running tea.Program.
//
// Important: Finish does NOT send cmdDoneMsg. That is only sent by
// execCommand when the Run function returns. This prevents the spinner
// from disappearing while the command is still running.
type StreamSink struct {
	p *tea.Program
}

func NewStreamSink(p *tea.Program) *StreamSink {
	return &StreamSink{p: p}
}

func (s *StreamSink) Print(text string) {
	s.p.Send(cmdOutputMsg{line: text})
}

func (s *StreamSink) PrintStyled(line string) {
	s.p.Send(cmdOutputMsg{line: line, preStyled: true})
}

func (s *StreamSink) PrintMarkdown(text string) {
	s.p.Send(cmdMarkdownLineMsg{line: text})
}

func (s *StreamSink) Status(text string) {
	s.p.Send(cmdStatusMsg{text: text})
}

func (s *StreamSink) Error(text string) {
	s.p.Send(cmdOutputMsg{line: text, isErr: true})
}

// Finish is a no-op for StreamSink. The cmdDoneMsg is sent by
// execCommand after Run returns, not by the command itself.
func (s *StreamSink) Finish(summary string) {
	if summary != "" {
		s.p.Send(cmdOutputMsg{line: summary})
	}
}

// CLISink writes directly to stdout for non-interactive mode.
type CLISink struct {
	w *bufio.Writer

	mdRenderer *glamour.TermRenderer
	mdWrap     int
}

func NewCLISink(out io.Writer) *CLISink {
	return &CLISink{w: bufio.NewWriter(out)}
}

func StdoutSink() *CLISink {
	return NewCLISink(os.Stdout)
}

func (s *CLISink) Print(text string) {
	fmt.Fprintln(s.w, text)
	s.w.Flush()
}

func (s *CLISink) PrintStyled(line string) {
	fmt.Fprintln(s.w, line)
	s.w.Flush()
}

func (s *CLISink) PrintMarkdown(text string) {
	if text == "" {
		fmt.Fprintln(s.w)
		s.w.Flush()
		return
	}
	wrap := mdrender.TermWidth(80) - 2
	if wrap < 20 {
		wrap = 20
	}
	if s.mdRenderer == nil || s.mdWrap != wrap {
		r, err := mdrender.NewRenderer(wrap)
		if err != nil {
			fmt.Fprintln(s.w, text)
			s.w.Flush()
			return
		}
		s.mdRenderer = r
		s.mdWrap = wrap
	}
	out, err := s.mdRenderer.Render(text)
	if err != nil {
		fmt.Fprintln(s.w, text)
	} else {
		fmt.Fprint(s.w, strings.TrimRight(out, "\n"))
		fmt.Fprintln(s.w)
	}
	s.w.Flush()
}

func (s *CLISink) Status(text string) {
	fmt.Fprintln(os.Stderr, text)
}

func (s *CLISink) Error(text string) {
	fmt.Fprintln(os.Stderr, "error:", text)
}

func (s *CLISink) Finish(summary string) {
	if summary != "" {
		fmt.Fprintln(s.w, summary)
	}
	s.w.Flush()
}
