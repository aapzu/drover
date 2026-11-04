package ui

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"
)

func selectModel(t *testing.T, txt string) Model {
	t.Helper()
	m := mouseModel(t)
	m.snap.Live = map[string]string{"c0-t0": "sess"}
	m.mode = modeLive
	m.previewFor, m.previewTxt, m.previewLines = "c0-t0", txt, len(captureLines(txt))
	return m
}

func TestDraggingOverThePreviewSelectsAndCopiesItsText(t *testing.T) {
	m := selectModel(t, "\x1b[31mfirst line\x1b[0m\nsecond line   \nthird\n\n")
	y := m.layout().previewY + 1

	m, _ = update(m, mousePress(7, y))
	m, _ = update(m, mouseMotion(6, y+1))
	next, cmd := m.Update(mouseRelease(6, y+1))
	m = next.(Model)

	if cmd == nil || !m.sel.active() || m.mode != modeLive {
		t.Fatalf("cmd %v sel %+v mode %v", cmd, m.sel, m.mode)
	}
	if got := m.selectedText(); got != "line\nsecond" {
		t.Errorf("selected %q", got)
	}
}

func TestDraggingUpwardsSelectsTheSameText(t *testing.T) {
	m := selectModel(t, "first line\nsecond line   \nthird")
	y := m.layout().previewY + 1

	m, _ = update(m, mousePress(6, y+2))
	m, _ = update(m, mouseMotion(7, y))
	if got := m.selectedText(); got != "line\nsecond line\nthird" {
		t.Errorf("selected %q", got)
	}
}

func TestClickingThePreviewWithoutDraggingCopiesNothing(t *testing.T) {
	m := selectModel(t, "text")
	y := m.layout().previewY + 1

	m, _ = update(m, mousePress(2, y))
	next, cmd := m.Update(mouseRelease(2, y))
	if cmd != nil || next.(Model).sel.active() {
		t.Errorf("cmd %v sel %+v", cmd, next.(Model).sel)
	}
}

func TestSelectionFollowsTheScrolledPreview(t *testing.T) {
	var lines []string
	for i := range 100 {
		lines = append(lines, strings.Repeat(string(rune('a'+i%26)), 3))
	}
	m := selectModel(t, strings.Join(lines, "\n"))
	m.previewHistory, m.previewScroll = 100, 10
	l := m.layout()
	start, _ := previewBounds(100, l.previewH-2, 10)

	p, ok := m.selPointAt(l, 1, l.previewY+1)
	if !ok || p != (selPoint{line: 99 - start, col: 0}) {
		t.Fatalf("point %+v, want line %d", p, 99-start)
	}

	m.sel = selection{anchor: p, head: selPoint{line: p.line, col: 2}}
	next, _ := m.Update(previewMsg{taskID: "c0-t0", content: m.previewTxt + "\nnew", history: 101})
	got := next.(Model)
	if got.sel.anchor.line != p.line+1 || got.selectedText() != lines[start] {
		t.Errorf("after new output: sel %+v text %q, want %q", got.sel, got.selectedText(), lines[start])
	}
}

func TestPreviewHighlightsTheSelection(t *testing.T) {
	m := selectModel(t, "\x1b[32mhello world\x1b[0m")
	m.sel = selection{anchor: selPoint{0, 6}, head: selPoint{0, 10}}

	body := m.previewBody(5)
	if ansi.Strip(body) != "hello world" || !strings.Contains(body, selStyle.Render("world")) {
		t.Errorf("body %q", body)
	}
}

func TestKeysAndOtherTasksClearTheSelection(t *testing.T) {
	m := selectModel(t, "text")
	m.sel = selection{head: selPoint{0, 2}}
	next, _ := m.Update(previewMsg{taskID: "c1-t0", content: "other"})
	if next.(Model).sel.active() {
		t.Error("another task's capture kept the selection")
	}

	m.mode = modeBoard
	next, _ = m.Update(key("z"))
	if next.(Model).sel.active() {
		t.Error("a key kept the selection")
	}
}

func TestCopiedFlash(t *testing.T) {
	if got := (copiedMsg{lines: 1}).flash(); got != "Copied 1 line." {
		t.Errorf("flash %q", got)
	}
	if got := (copiedMsg{lines: 3, err: errors.New("no xclip")}).flash(); !strings.Contains(got, "3 lines") || !strings.Contains(got, "system clipboard failed") {
		t.Errorf("flash %q", got)
	}
}

func TestReleasingCopiesTheSelectedText(t *testing.T) {
	var term strings.Builder
	var copied string
	terminalOut, systemClipboard = &term, func(s string) error { copied = s; return nil }
	t.Cleanup(func() { terminalOut, systemClipboard = os.Stdout, clipboard.WriteAll })

	m := selectModel(t, "one\ntwo")
	y := m.layout().previewY + 1
	m, _ = update(m, mousePress(1, y))
	m, _ = update(m, mouseMotion(3, y+1))
	_, cmd := m.Update(mouseRelease(3, y+1))
	if cmd == nil {
		t.Fatal("release copied nothing")
	}
	m, _ = update(m, cmd())
	if copied != "one\ntwo" || term.String() != ansi.SetSystemClipboard("one\ntwo") || m.flash != "Copied 2 lines." {
		t.Errorf("copied %q, terminal %q, flash %q", copied, term.String(), m.flash)
	}
}

func TestDraggingPastTheTextCopiesNothing(t *testing.T) {
	m := selectModel(t, "abc")
	y := m.layout().previewY + 1
	m, _ = update(m, mousePress(20, y))
	m, _ = update(m, mouseMotion(30, y))
	if _, cmd := m.Update(mouseRelease(30, y)); cmd != nil {
		t.Error("an empty selection was copied")
	}
}

func TestPressingAPreviewWithoutAnAgentSelectsNothing(t *testing.T) {
	m := selectModel(t, "stale capture")
	m.snap.Live = nil
	m.mode = modeBoard
	y := m.layout().previewY + 1
	m, _ = update(m, mousePress(1, y))
	m, _ = update(m, mouseMotion(5, y))
	if m.sel.active() || m.drag.kind == dragSelect {
		t.Errorf("sel %+v drag %v", m.sel, m.drag.kind)
	}
}

func TestDraggingOntoTheBorderScrollsThePreview(t *testing.T) {
	var lines []string
	for i := range 100 {
		lines = append(lines, fmt.Sprint(i))
	}
	m := selectModel(t, strings.Join(lines, "\n"))
	m.previewHistory, m.previewScroll, m.previewLines, m.previewComplete = 100, 5, 100, true
	l := m.layout()

	m, _ = update(m, mousePress(1, l.previewY+2))
	m, _ = update(m, mouseMotion(1, l.previewY))
	if m.previewScroll != 6 || !m.sel.active() || m.sel.head.line != 6+l.previewH-3 {
		t.Fatalf("up: scroll %d sel %+v", m.previewScroll, m.sel)
	}
	m, _ = update(m, mouseMotion(1, l.previewY+l.previewH-1))
	if m.previewScroll != 5 || m.sel.head.line != 5 {
		t.Errorf("down: scroll %d sel %+v", m.previewScroll, m.sel)
	}
}

func TestPreviewHighlightsSelectedLinesWhileScrolled(t *testing.T) {
	m := selectModel(t, "a\nbb\ncc\nd\ne")
	m.previewHistory, m.previewScroll = 5, 1
	m.sel = selection{anchor: selPoint{3, 1}, head: selPoint{1, 0}}

	got := strings.Split(m.previewBody(3), "\n")
	want := []string{"b" + selStyle.Render("b"), selStyle.Render("cc"), selStyle.Render("d")}
	if !slices.Equal(got, want) {
		t.Errorf("body %q, want %q", got, want)
	}
}
