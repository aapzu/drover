package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/aapzu/drover/internal/engine"
)

func askModel(t *testing.T) Model {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := testModel(100, 30, 2, 1)
	m.askInput = newAskInput()
	m.askView = viewport.New(80, 20)
	m.eng = &engine.Engine{Root: t.TempDir()}
	return m
}

func typeAsk(m Model, text string) Model {
	m.askInput.SetValue(text)
	return m
}

func fakeAssistant(t *testing.T, answer string, err error) *[]string {
	t.Helper()
	var asked []string
	orig := askAssistant
	t.Cleanup(func() { askAssistant = orig })
	askAssistant = func(_ context.Context, _, _, question string, fresh bool) (string, error) {
		asked = append(asked, question)
		return answer, err
	}
	return &asked
}

func TestAskKeyOpensPaneAndEscClosesIt(t *testing.T) {
	m := askModel(t)
	next, _ := m.Update(key(askKey))
	m = next.(Model)
	if m.mode != modeAsk {
		t.Fatalf("mode = %v, want ask", m.mode)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if next.(Model).mode != modeBoard {
		t.Error("esc should return to the board")
	}
}

func TestAskTypingGoesToTheQuestionNotTheBoard(t *testing.T) {
	m := askModel(t)
	next, _ := m.Update(key(askKey))
	m = next.(Model)
	for _, k := range []string{"q", "j", "n"} {
		next, _ = m.Update(key(k))
		m = next.(Model)
	}
	if m.mode != modeAsk || m.askInput.Value() != "qjn" {
		t.Errorf("mode %v, input %q: keys leaked to the board", m.mode, m.askInput.Value())
	}
}

func TestAnswerLandsUnderItsQuestion(t *testing.T) {
	m := askModel(t)
	m.askLog = []askEntry{{question: "where is #1?"}, {question: "and #2?"}}
	m.askBusy = true
	m.mode = modeAsk
	next, _ := m.Update(askDoneMsg{answer: "In Col 1"})
	m = next.(Model)

	if m.askBusy {
		t.Error("still busy after the answer")
	}
	if got := m.askLog[1].answer; got != "In Col 1" || m.askLog[0].answer != "" {
		t.Errorf("answers = %q, %q", m.askLog[0].answer, got)
	}
	if view := m.renderAskLog(); !strings.Contains(view, "In Col 1") || !strings.Contains(view, "and #2?") {
		t.Errorf("log lacks the exchange:\n%s", view)
	}
}

func TestAnswerArrivingAfterLeavingTheBoardFlashes(t *testing.T) {
	for name, tc := range map[string]struct {
		msg  askDoneMsg
		want string
	}{
		"answer": {askDoneMsg{answer: "In Col 1"}, "answered; press " + askKey},
		"error":  {askDoneMsg{err: errors.New("boom")}, "failed; press " + askKey},
	} {
		m := askModel(t)
		m.mode = modeBoard
		m.askLog = []askEntry{{question: "q"}}
		m.askBusy = true
		next, _ := m.Update(tc.msg)
		m = next.(Model)
		if !strings.Contains(m.flash, tc.want) {
			t.Errorf("%s: flash = %q, want %q", name, m.flash, tc.want)
		}
		if tc.msg.err != nil && !strings.Contains(m.renderAskLog(), "boom") {
			t.Errorf("%s: log lacks the error:\n%s", name, m.renderAskLog())
		}
	}
}

func TestEnterAsksOnceAndFilesTheAnswer(t *testing.T) {
	asked := fakeAssistant(t, "In Col 1", nil)
	m := askModel(t)
	m.mode = modeAsk
	m.askInput.Focus()

	next, cmd := typeAsk(m, "  where is #1?  ").Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if !m.askBusy || len(m.askLog) != 1 || m.askLog[0].question != "where is #1?" || m.askInput.Value() != "" || cmd == nil {
		t.Fatalf("after enter: busy=%v log=%+v input=%q cmd=%v", m.askBusy, m.askLog, m.askInput.Value(), cmd != nil)
	}

	if _, again := typeAsk(m, "and #2?").Update(tea.KeyMsg{Type: tea.KeyEnter}); again != nil {
		t.Error("a second question was sent while the first was waiting")
	}
	if len(m.askLog) != 1 {
		t.Errorf("log = %+v", m.askLog)
	}

	done := cmd().(askDoneMsg)
	next, _ = m.Update(done)
	m = next.(Model)
	if len(*asked) != 1 || (*asked)[0] != "where is #1?" || m.askLog[0].answer != "In Col 1" || m.askBusy {
		t.Errorf("asked %q, log %+v, busy %v", *asked, m.askLog, m.askBusy)
	}
}

func TestEnterIgnoresABlankQuestion(t *testing.T) {
	m := askModel(t)
	m.mode = modeAsk
	next, cmd := typeAsk(m, "   ").Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || next.(Model).askBusy || len(next.(Model).askLog) != 0 {
		t.Error("a blank question was sent")
	}
}

func TestCtrlNForgetsTheConversationInTheBackground(t *testing.T) {
	m := askModel(t)
	m.mode = modeAsk
	m.askLog = []askEntry{{question: "q", answer: "a"}}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = next.(Model)
	if cmd == nil || !m.askBusy || len(m.askLog) != 1 {
		t.Fatalf("ctrl+n should reset in a command and keep the log until it is done: cmd=%v busy=%v log=%v", cmd != nil, m.askBusy, m.askLog)
	}
	if _, again := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN}); again != nil {
		t.Error("ctrl+n ran twice")
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.askBusy || len(m.askLog) != 0 {
		t.Errorf("after the reset: busy=%v log=%v", m.askBusy, m.askLog)
	}
}

func TestCtrlNDoesNothingWhileAQuestionIsRunning(t *testing.T) {
	m := askModel(t)
	m.mode, m.askBusy = modeAsk, true
	m.askLog = []askEntry{{question: "q"}}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN}); cmd != nil {
		t.Error("reset while a question is running")
	}
}

func TestFailedResetIsShownInTheLog(t *testing.T) {
	m := askModel(t)
	m.mode, m.askBusy = modeAsk, true
	m.askLog = []askEntry{{question: "q", answer: "a"}}
	next, _ := m.Update(askResetMsg{err: errors.New("disk full")})
	m = next.(Model)
	if len(m.askLog) != 2 || !strings.Contains(m.renderAskLog(), "disk full") {
		t.Errorf("log = %+v", m.askLog)
	}
}

func TestAnswerAndSaveWarningAppearTogether(t *testing.T) {
	m := askModel(t)
	m.askLog = []askEntry{{question: "q"}}
	m.askBusy = true
	next, _ := m.Update(askDoneMsg{answer: "moved #2", err: errors.New("could not save the conversation")})
	log := next.(Model).renderAskLog()
	if !strings.Contains(log, "moved #2") || !strings.Contains(log, "could not save the conversation") {
		t.Errorf("log:\n%s", log)
	}
}
