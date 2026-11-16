package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// selPoint is a cell of a pane's capture. line counts up from the capture's
// last line, so a point stays on its text as output arrives.
type selPoint struct{ line, col int }

// before reports whether a comes first in reading order.
func (a selPoint) before(b selPoint) bool {
	return a.line > b.line || (a.line == b.line && a.col < b.col)
}

type selTarget uint8

const (
	selPreview selTarget = iota
	selAssistant
)

type selUnit uint8

const (
	selCell selUnit = iota
	selWord
	selLine
)

// selection runs from anchor to head.
type selection struct {
	anchor, head selPoint
	unit         selUnit
	origin       [2]selPoint
}

func (s selection) active() bool { return s.anchor != s.head || s.unit != selCell }

// shift moves the selection by d lines when output arrives below it.
func (s *selection) shift(d int) {
	s.anchor.line += d
	s.head.line += d
	s.origin[0].line += d
	s.origin[1].line += d
}

// bounds returns the selection's first and last cell in reading order.
func (s selection) bounds() (selPoint, selPoint) {
	a, b := s.anchor, s.head
	if b.before(a) {
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

// selPane is a pane whose text can be selected: the text it shows, how far it
// is scrolled up, and the screen cells its window covers.
type selPane struct {
	txt          string
	scroll       int
	sel          selection
	x0, y0, w, h int
}

// pointAt maps a screen cell to the pane's text, clamping it into the window.
func (p selPane) pointAt(x, y int) (selPoint, bool) {
	lines := captureLines(p.txt)
	if len(lines) == 0 || p.h <= 0 || p.w <= 0 {
		return selPoint{}, false
	}
	start, _ := previewBounds(len(lines), p.h, p.scroll)
	idx := min(start+clamp(y-p.y0, 0, p.h-1), len(lines)-1)
	return selPoint{line: len(lines) - 1 - idx, col: clamp(x-p.x0, 0, p.w-1)}, true
}

// selectedText is the selection's plain text, without trailing spaces.
func (p selPane) selectedText() string {
	lines := captureLines(p.txt)
	var out []string
	for i, line := range lines {
		from, to, ok := p.sel.span(len(lines) - 1 - i)
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

// body is the window of n lines the pane shows, with the selection highlighted.
func (p selPane) body(n int) string {
	lines := captureLines(p.txt)
	start, end := previewBounds(len(lines), n, p.scroll)
	return strings.Join(p.highlight(lines[start:end], len(lines)-end), "\n")
}

// highlight reverses the selected part of the window's lines, the last of
// which is end lines above the capture's last line.
func (p selPane) highlight(window []string, end int) []string {
	for i, line := range window {
		from, to, ok := p.sel.span(end + len(window) - 1 - i)
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

// unitAt is the cells [from, to) of the word or line around pt.
func (p selPane) unitAt(u selUnit, pt selPoint) (from, to int, ok bool) {
	lines := captureLines(p.txt)
	i := len(lines) - 1 - pt.line
	if i < 0 || i >= len(lines) {
		return 0, 0, false
	}
	plain := ansi.Strip(lines[i])
	if u == selLine {
		to = ansi.StringWidth(strings.TrimRight(plain, " "))
		return 0, to, to > 0
	}
	return wordSpan(plain, pt.col)
}

// selectUnit selects the word or line around pt.
func (p selPane) selectUnit(u selUnit, pt selPoint) (selection, bool) {
	from, to, ok := p.unitAt(u, pt)
	if !ok {
		return selection{}, false
	}
	lo, hi := selPoint{pt.line, from}, selPoint{pt.line, to - 1}
	return selection{anchor: lo, head: hi, unit: u, origin: [2]selPoint{lo, hi}}, true
}

// extend moves the selection's end to pt, by whole words or lines when it
// started from a double or triple click.
func (p selPane) extend(s selection, pt selPoint) selection {
	if s.unit == selCell {
		s.head = pt
		return s
	}
	from, to, ok := p.unitAt(s.unit, pt)
	if !ok {
		from, to = pt.col, pt.col+1
	}
	lo, hi := selPoint{pt.line, from}, selPoint{pt.line, to - 1}
	if lo.before(s.origin[0]) {
		s.anchor, s.head = s.origin[1], lo
	} else {
		s.anchor, s.head = s.origin[0], hi
	}
	return s
}

const wordPunct = "_-./~:@%+#="

func wordChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(wordPunct, r)
}

// wordSpan is the cells [from, to) of the word around col in a plain line:
// a run of word characters, so paths, URLs and flags stay whole; a run of
// spaces; or a run of the same punctuation mark.
func wordSpan(line string, col int) (from, to int, ok bool) {
	var cells []rune
	for _, r := range line {
		for range ansi.StringWidth(string(r)) {
			cells = append(cells, r)
		}
	}
	if col < 0 || col >= len(cells) {
		return 0, 0, false
	}
	c, word := cells[col], wordChar(cells[col])
	same := func(r rune) bool {
		if word {
			return wordChar(r)
		}
		return r == c
	}
	from, to = col, col+1
	for from > 0 && same(cells[from-1]) {
		from--
	}
	for to < len(cells) && same(cells[to]) {
		to++
	}
	if word {
		for to-from > 1 && to > col+1 && strings.ContainsRune(".:", cells[to-1]) {
			to--
		}
	}
	return from, to, true
}

// selClick tracks the press in a row of clicks on the same cell.
type selClick struct {
	at     time.Time
	pane   selTarget
	x, y   int
	clicks int
}

// countClick is how many clicks in a row have landed on the cell: 1 for a
// single click, 2 for a double, 3 for a triple, and 1 again for the next.
func (m *Model) countClick(t selTarget, x, y int) int {
	c := m.selClick
	if c.clicks > 0 && c.pane == t && c.x == x && c.y == y && time.Since(c.at) < doubleClick {
		c.clicks = c.clicks%3 + 1
	} else {
		c.clicks = 1
	}
	c.at, c.pane, c.x, c.y = time.Now(), t, x, y
	m.selClick = c
	return c.clicks
}

func (m *Model) setSel(t selTarget, s selection) {
	if t == selAssistant {
		m.assist.sel = s
		return
	}
	m.sel = s
}

// pane is the selectable pane t, as laid out in l.
func (m Model) pane(t selTarget, l layout) selPane {
	if t == selAssistant {
		return selPane{txt: m.assist.txt, scroll: m.assist.scroll, sel: m.assist.sel,
			x0: l.boardW + 1, y0: l.bodyY + 1, w: l.assistantW - 2, h: l.bodyH - 2}
	}
	txt, scroll := m.previewText(l.previewH - 2)
	return selPane{txt: txt, scroll: scroll, sel: m.sel, x0: 1, y0: l.previewY + 1, w: l.boardW - 2, h: l.previewH - 2}
}

// beginSelect starts a selection at the cell under a press in pane t: a click
// selects nothing yet, a double click the word and a triple click the line.
func (m Model) beginSelect(l layout, t selTarget, x, y int) Model {
	p := m.pane(t, l)
	pt, ok := p.pointAt(x, y)
	if !ok {
		return m
	}
	s := selection{anchor: pt, head: pt}
	switch m.countClick(t, x, y) {
	case 2:
		if w, ok := p.selectUnit(selWord, pt); ok {
			s = w
		}
	case 3:
		if ln, ok := p.selectUnit(selLine, pt); ok {
			s = ln
		}
	}
	m.drag.kind, m.drag.sel = dragSelect, t
	m.setSel(t, s)
	return m
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
