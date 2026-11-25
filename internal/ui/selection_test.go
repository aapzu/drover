package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/store"
)

func selectModel(t *testing.T, txt string) Model {
	t.Helper()
	m := mouseModel(t)
	m.snap.Live = map[string]string{"c0-t0": "sess"}
	m.mode = modeLive
	m.previewFor, m.previewTxt, m.previewLines = "c0-t0", txt, len(captureLines(txt))
	return m
}

func selText(m Model) string { return m.pane(selPreview, m.layout()).selectedText() }

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
	if got := selText(m); got != "line\nsecond" {
		t.Errorf("selected %q", got)
	}
}

func TestDraggingUpwardsSelectsTheSameText(t *testing.T) {
	m := selectModel(t, "first line\nsecond line   \nthird")
	y := m.layout().previewY + 1

	m, _ = update(m, mousePress(6, y+2))
	m, _ = update(m, mouseMotion(7, y))
	if got := selText(m); got != "line\nsecond line\nthird" {
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

	p, ok := m.pane(selPreview, l).pointAt(1, l.previewY+1)
	if !ok || p != (selPoint{line: 99 - start, col: 0}) {
		t.Fatalf("point %+v, want line %d", p, 99-start)
	}

	m.sel = selection{anchor: p, head: selPoint{line: p.line, col: 2}}
	next, _ := m.Update(previewMsg{taskID: "c0-t0", content: m.previewTxt + "\nnew", history: 101})
	got := next.(Model)
	if got.sel.anchor.line != p.line+1 || selText(got) != lines[start] {
		t.Errorf("after new output: sel %+v text %q, want %q", got.sel, selText(got), lines[start])
	}
}

func TestPreviewHighlightsTheSelection(t *testing.T) {
	m := selectModel(t, "\x1b[32mhello world\x1b[0m")
	m.sel = selection{anchor: selPoint{0, 6}, head: selPoint{0, 10}}

	body := selPane{txt: m.previewTxt, sel: m.sel}.body(5)
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

func TestPreviewWithoutAnAgentSelectsItsNoteNotTheStaleCapture(t *testing.T) {
	m := selectModel(t, "stale capture")
	m.snap.Live = nil
	m.mode = modeBoard
	y := m.layout().previewY + 1
	m, _ = update(m, mousePress(1, y))
	m, _ = update(m, mouseMotion(5, y))
	if got := selText(m); got != "No ru" {
		t.Errorf("selected %q", got)
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

	got := strings.Split(selPane{txt: m.previewTxt, scroll: m.previewScroll, sel: m.sel}.body(3), "\n")
	want := []string{"b" + selStyle.Render("b"), selStyle.Render("cc"), selStyle.Render("d")}
	if !slices.Equal(got, want) {
		t.Errorf("body %q, want %q", got, want)
	}
}

func TestWordSpan(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		col      int
		from, to int
		ok       bool
	}{
		{"plain word", "run go test now", 8, 7, 11, true},
		{"path stays whole", "open ./internal/ui/mouse.go now", 10, 5, 27, true},
		{"trailing dot is dropped", "see ./a/b.go.", 6, 4, 12, true},
		{"url stops at the query", "at https://x.dev/a?b=1 ok", 6, 3, 18, true},
		{"spaces", "a    b", 2, 1, 5, true},
		{"punctuation run", "a ──── b", 4, 2, 6, true},
		{"brackets end a word", "f(arg)", 3, 2, 5, true},
		{"wide runes take two cells", "日本語 x", 3, 0, 6, true},
		{"past the text", "abc", 3, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			from, to, ok := wordSpan(tt.line, tt.col)
			if from != tt.from || to != tt.to || ok != tt.ok {
				t.Errorf("wordSpan(%q, %d) = %d, %d, %v; want %d, %d, %v", tt.line, tt.col, from, to, ok, tt.from, tt.to, tt.ok)
			}
		})
	}
}

func click(m Model, x, y int) Model {
	m, _ = update(m, mousePress(x, y))
	m, _ = update(m, mouseRelease(x, y))
	return m
}

func TestDoubleClickSelectsAWordAndTripleClickTheLine(t *testing.T) {
	m := selectModel(t, "run go test now\nsecond line")
	x, y := 9, m.layout().previewY+1

	m = click(m, x, y)
	if m.sel.active() {
		t.Fatalf("a single click selected %q", selText(m))
	}
	m = click(m, x, y)
	if got := selText(m); got != "test" {
		t.Fatalf("double click selected %q", got)
	}
	m = click(m, x, y)
	if got := selText(m); got != "run go test now" {
		t.Fatalf("triple click selected %q", got)
	}
	m = click(m, x, y)
	if m.sel.active() {
		t.Errorf("a fourth click kept %q selected", selText(m))
	}
}

func TestDoubleClickCopiesTheWordOnRelease(t *testing.T) {
	var copied string
	terminalOut, systemClipboard = io.Discard, func(s string) error { copied = s; return nil }
	t.Cleanup(func() { terminalOut, systemClipboard = os.Stdout, clipboard.WriteAll })

	m := selectModel(t, "run go test now")
	x, y := 9, m.layout().previewY+1
	m = click(m, x, y)
	m, _ = update(m, mousePress(x, y))
	_, cmd := m.Update(mouseRelease(x, y))
	if cmd == nil {
		t.Fatal("release copied nothing")
	}
	cmd()
	if copied != "test" {
		t.Errorf("copied %q", copied)
	}
}

func TestSlowOrMovedClicksStayASingleClick(t *testing.T) {
	m := selectModel(t, "run go test now")
	x, y := 9, m.layout().previewY+1

	m = click(m, x, y)
	m.selClick.at = m.selClick.at.Add(-2 * doubleClick)
	if m = click(m, x, y); m.sel.active() {
		t.Error("a slow second click selected")
	}
	m = click(m, x, y)
	if !m.sel.active() {
		t.Fatal("a quick second click did not select")
	}
	if m = click(m, x+1, y); m.sel.active() {
		t.Error("a click on another cell selected")
	}
}

func TestDraggingAfterADoubleClickExtendsByWords(t *testing.T) {
	m := selectModel(t, "alpha beta gamma delta")
	y := m.layout().previewY + 1
	m = click(m, 8, y)
	m, _ = update(m, mousePress(8, y))
	if got := selText(m); got != "beta" {
		t.Fatalf("selected %q", got)
	}
	m, _ = update(m, mouseMotion(19, y))
	if got := selText(m); got != "beta gamma delta" {
		t.Errorf("forwards: %q", got)
	}
	m, _ = update(m, mouseMotion(2, y))
	if got := selText(m); got != "alpha beta" {
		t.Errorf("backwards: %q", got)
	}
}

func TestDraggingAfterATripleClickExtendsByLines(t *testing.T) {
	m := selectModel(t, "one two\nthree four\nfive")
	y := m.layout().previewY + 1
	m = click(m, 3, y)
	m = click(m, 3, y)
	m, _ = update(m, mousePress(3, y))
	m, _ = update(m, mouseMotion(2, y+1))
	if got := selText(m); got != "one two\nthree four" {
		t.Errorf("selected %q", got)
	}
}

func TestClickingPastTheEndOfALineSelectsNoWord(t *testing.T) {
	m := selectModel(t, "abc")
	x, y := 30, m.layout().previewY+1
	m = click(m, x, y)
	if m = click(m, x, y); m.sel.active() {
		t.Errorf("selected %q", selText(m))
	}
}

func TestSelectingTheNoteOfACardWithoutALiveAgent(t *testing.T) {
	m := selectModel(t, "stale capture")
	m.snap.Live = nil
	m.mode = modeBoard
	y := m.layout().previewY + 1

	m = click(m, 1, y)
	m = click(m, 1, y)
	if got := selText(m); got != "No" {
		t.Errorf("selected %q", got)
	}
}

func TestSelectingTheLastReplyOfASuspendedCard(t *testing.T) {
	m := selectModel(t, "stale capture")
	m.snap.Live = nil
	m.mode = modeBoard
	m.current().Sessions = map[string]*store.Session{store.MainSession: {
		Status: store.StatusSuspended, LastReply: "Fixed the bug in ./internal/ui/mouse.go.",
	}}
	l := m.layout()
	if !strings.Contains(m.pane(selPreview, l).txt, "Fixed the bug") {
		t.Fatalf("the preview shows %q", m.pane(selPreview, l).txt)
	}

	x, y := 20, l.previewY+4
	m = click(m, x, y)
	m = click(m, x, y)
	if got := selText(m); got != "./internal/ui/mouse.go" {
		t.Errorf("selected %q", got)
	}
	m = click(m, x, y)
	if got := selText(m); got != "Fixed the bug in ./internal/ui/mouse.go." {
		t.Errorf("triple click selected %q", got)
	}
	if got := ansi.Strip(m.renderPreview(l.previewH)); !strings.Contains(got, "Fixed the bug") {
		t.Errorf("preview %q", got)
	}
}

func assistantWithText(t *testing.T, txt string) Model {
	t.Helper()
	fakePanel(t)
	m := open(t, assistantModel(t))
	m, _ = update(m, m.stamped(assistantFrameMsg{txt: txt, at: time.Now()}))
	return m
}

func assistantSelText(m Model) string { return m.pane(selAssistant, m.layout()).selectedText() }

func TestDraggingOverTheAssistantSelectsOnlyItsText(t *testing.T) {
	m := assistantWithText(t, "first line\nsecond line\nthird")
	l := m.layout()
	x, y := l.boardW+1, l.bodyY+1

	m, _ = update(m, mousePress(x+6, y))
	m, _ = update(m, mouseMotion(x+5, y+1))
	next, cmd := m.Update(mouseRelease(x+5, y+1))
	m = next.(Model)

	if cmd == nil || m.mode != modeAssistant {
		t.Fatalf("cmd %v mode %v", cmd, m.mode)
	}
	if got := assistantSelText(m); got != "line\nsecond" {
		t.Errorf("selected %q", got)
	}
	if m.sel.active() {
		t.Error("the preview selected too")
	}
}

func TestDraggingOutsideTheAssistantTextStaysInsideItsWindow(t *testing.T) {
	m := assistantWithText(t, "abc\ndef")
	l := m.layout()
	m, _ = update(m, mousePress(l.boardW+1, l.bodyY+1))
	m, _ = update(m, mouseMotion(m.width+10, l.bodyY+2))
	if got := assistantSelText(m); got != "abc\ndef" {
		t.Errorf("selected %q", got)
	}
}

func TestDoubleClickInTheAssistantSelectsAWord(t *testing.T) {
	m := assistantWithText(t, "run go test now")
	l := m.layout()
	x, y := l.boardW+9, l.bodyY+1
	m = click(m, x, y)
	m = click(m, x, y)
	if got := assistantSelText(m); got != "test" {
		t.Errorf("selected %q", got)
	}
}

func TestAssistantSelectionFollowsNewOutputAndClearsOnKeys(t *testing.T) {
	m := assistantWithText(t, "one\ntwo")
	l := m.layout()
	m, _ = update(m, mousePress(l.boardW+1, l.bodyY+1))
	m, _ = update(m, mouseMotion(l.boardW+3, l.bodyY+1))
	if got := assistantSelText(m); got != "one" {
		t.Fatalf("selected %q", got)
	}

	m, _ = update(m, m.stamped(assistantFrameMsg{txt: "one\ntwo\nthree", history: 1, at: time.Now().Add(time.Second)}))
	if got := assistantSelText(m); got != "one" {
		t.Errorf("after new output selected %q", got)
	}
	m, _ = update(m, key("a"))
	if m.assist.sel.active() {
		t.Error("a key kept the selection")
	}
}

func TestDraggingOntoTheAssistantBorderScrollsIt(t *testing.T) {
	fakePanel(t)
	var lines []string
	for i := range 100 {
		lines = append(lines, fmt.Sprint(i))
	}
	m := open(t, assistantModel(t))
	m, _ = update(m, m.stamped(assistantFrameMsg{txt: strings.Join(lines, "\n"), history: 100, at: time.Now()}))
	l := m.layout()

	m, _ = update(m, mousePress(l.boardW+1, l.bodyY+2))
	m, cmd := update(m, mouseMotion(l.boardW+1, l.bodyY))
	if m.assist.scroll != 1 || cmd == nil || !m.assist.sel.active() {
		t.Errorf("scroll %d cmd nil %v sel %+v", m.assist.scroll, cmd == nil, m.assist.sel)
	}
}

func TestPressingAStoppedAssistantSelectsNothing(t *testing.T) {
	m := assistantWithText(t, "text")
	m.assist.fail(errors.New("gone"))
	l := m.layout()
	m, _ = update(m, mousePress(l.boardW+1, l.bodyY+1))
	m, _ = update(m, mouseMotion(l.boardW+3, l.bodyY+1))
	if m.assist.sel.active() {
		t.Errorf("sel %+v", m.assist.sel)
	}
}

func TestNoteSelectionMatchesTabsAndTallNotes(t *testing.T) {
	m := selectModel(t, "stale capture")
	m.snap.Live = nil
	m.mode = modeBoard
	l := m.layout()
	reply := "first\n\tfmt.Println(x)"
	for range l.previewH {
		reply += "\nfiller"
	}
	reply += "\n\tlast.line()"
	m.current().Sessions = map[string]*store.Session{store.MainSession: {Status: store.StatusSuspended, LastReply: reply}}

	rows := strings.Split(ansi.Strip(m.renderPreview(l.previewH)), "\n")
	last := rows[len(rows)-2]
	col := strings.Index(last, "last.line()")
	if col < 0 {
		t.Fatalf("the preview ends with %q, not the reply's last line", last)
	}
	y := l.previewY + l.previewH - 2
	m = click(m, col+1, y)
	m = click(m, col+1, y)
	if got := selText(m); got != "last.line" {
		t.Errorf("selected %q", got)
	}
}

func TestForwardedClickClearsTheAssistantSelection(t *testing.T) {
	m := trackingModel()
	m.assist.sel = selection{anchor: selPoint{0, 0}, head: selPoint{0, 3}}
	m, _ = update(m, mousePress(5, m.layout().previewY+3))
	if m.assist.sel.active() {
		t.Errorf("sel %+v", m.assist.sel)
	}
}
