package ui

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// selPoint is a cell of the preview capture. line counts up from the
// capture's last line, so a point stays on its text as output arrives.
type selPoint struct{ line, col int }

type selection struct{ anchor, head selPoint }

func (s selection) active() bool { return s.anchor != s.head }

// bounds returns the selection's first and last cell in reading order.
func (s selection) bounds() (selPoint, selPoint) {
	a, b := s.anchor, s.head
	if a.line < b.line || (a.line == b.line && a.col > b.col) {
		a, b = b, a
	}
	return a, b
}

// span is the columns [from, to) the selection covers on the line counted
// from the end, or false when it doesn't touch it.
func (s selection) span(line int) (int, int, bool) {
	if !s.active() {
		return 0, 0, false
	}
	a, b := s.bounds()
	if line > a.line || line < b.line {
		return 0, 0, false
	}
	from, to := 0, -1
	if line == a.line {
		from = a.col
	}
	if line == b.line {
		to = b.col + 1
	}
	return from, to, true
}

var selStyle = lipgloss.NewStyle().Reverse(true)

// selPointAt maps a screen cell inside the preview to the capture cell it shows.
func (m Model) selPointAt(l layout, x, y int) (selPoint, bool) {
	n := l.previewH - 2
	if m.previewLines == 0 || n <= 0 {
		return selPoint{}, false
	}
	start, _ := previewBounds(m.previewLines, n, m.previewScroll)
	idx := min(start+clamp(y-l.previewY-1, 0, n-1), m.previewLines-1)
	return selPoint{line: m.previewLines - 1 - idx, col: clamp(x-1, 0, l.boardW-3)}, true
}

// selectedText is the selection's plain text, without trailing spaces.
func (m Model) selectedText() string {
	lines := captureLines(m.previewTxt)
	var out []string
	for i, line := range lines {
		from, to, ok := m.sel.span(len(lines) - 1 - i)
		if !ok {
			continue
		}
		plain := ansi.Strip(line)
		if to < 0 {
			to = ansi.StringWidth(plain)
		}
		out = append(out, strings.TrimRight(ansi.Cut(plain, from, to), " "))
	}
	return strings.Join(out, "\n")
}

// highlight reverses the selected part of the window's lines, the last of
// which is end lines above the capture's last line.
func (m Model) highlight(window []string, end int) []string {
	for i, line := range window {
		from, to, ok := m.sel.span(end + len(window) - 1 - i)
		if !ok {
			continue
		}
		w := ansi.StringWidth(line)
		if to < 0 || to > w {
			to = w
		}
		if from >= to {
			continue
		}
		window[i] = ansi.Cut(line, 0, from) + selStyle.Render(ansi.Strip(ansi.Cut(line, from, to))) + ansi.Cut(line, to, w)
	}
	return window
}

type copiedMsg struct {
	lines int
	err   error
}

var (
	terminalOut     io.Writer = os.Stdout
	systemClipboard           = clipboard.WriteAll
)

// copyText puts text on the terminal's clipboard with OSC 52, which works
// over SSH, and on the local system clipboard.
func copyText(text string) tea.Cmd {
	return func() tea.Msg {
		_, err := io.WriteString(terminalOut, ansi.SetSystemClipboard(text))
		if sysErr := systemClipboard(text); err == nil || sysErr == nil {
			err = sysErr
		}
		return copiedMsg{lines: strings.Count(text, "\n") + 1, err: err}
	}
}

func (msg copiedMsg) flash() string {
	s := fmt.Sprintf("Copied %d line", msg.lines)
	if msg.lines != 1 {
		s += "s"
	}
	if msg.err != nil {
		return s + " to the terminal only; the system clipboard failed: " + msg.err.Error()
	}
	return s + "."
}
