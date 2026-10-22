package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aapzu/drover/internal/tmux"
)

// liveExitKey is the only key live mode and the assistant panel keep; it matches
// tmux.DetachKey.
// Option chords can't be reserved: macOS types them as characters such as å.
const liveExitKey = "ctrl+q"

var tmuxKeyNames = map[string]string{
	"enter":     "Enter",
	"tab":       "Tab",
	"shift+tab": "BTab",
	"backspace": "BSpace",
	"delete":    "DC",
	"esc":       "Escape",
	"up":        "Up",
	"down":      "Down",
	"left":      "Left",
	"right":     "Right",
	"home":      "Home",
	"end":       "End",
	"pgup":      "PPage",
	"pgdown":    "NPage",
	"insert":    "IC",
	" ":         "Space",
}

// tmuxKey translates a key press into either literal text or a tmux key name.
func tmuxKey(msg tea.KeyMsg) (literal, key string) {
	if msg.Type == tea.KeyRunes && !msg.Alt {
		return string(msg.Runes), ""
	}
	if msg.Type == tea.KeySpace && !msg.Alt {
		return " ", ""
	}
	var mods strings.Builder
	s := msg.String()
	for _, p := range []struct{ tea, tmux string }{{"alt+", "M-"}, {"ctrl+", "C-"}, {"shift+", "S-"}} {
		if name, ok := tmuxKeyNames[s]; ok {
			return "", mods.String() + name
		}
		if rest, ok := strings.CutPrefix(s, p.tea); ok {
			mods.WriteString(p.tmux)
			s = rest
		}
	}
	switch {
	case tmuxKeyNames[s] != "":
		s = tmuxKeyNames[s]
	case len(s) >= 2 && s[0] == 'f' && s[1] >= '1' && s[1] <= '9':
		s = "F" + s[1:]
	case len([]rune(s)) != 1:
		return "", ""
	}
	return "", mods.String() + s
}

func keyJobFor(session string, msg tea.KeyMsg) (job keyJob, ok bool) {
	job = keyJob{session: session}
	if msg.Paste {
		job.paste = string(msg.Runes)
		return job, true
	}
	job.literal, job.key = tmuxKey(msg)
	return job, job.literal != "" || job.key != ""
}

func afterKey(capture tea.Cmd) tea.Cmd {
	if capture == nil {
		return nil
	}
	return func() tea.Msg {
		time.Sleep(40 * time.Millisecond)
		return capture()
	}
}

func wheelJob(session string, up bool, x, y int) keyJob {
	code := 65
	if up {
		code = 64
	}
	return keyJob{session: session, literal: fmt.Sprintf("\x1b[<%d;%d;%dM", code, x, y)}
}

type keyJob struct {
	session, literal, key, paste string
}

// keySender sends keystrokes to tmux one at a time, in order, merging runs
// of typed text into one send-keys call.
type keySender struct {
	jobs chan keyJob
	errs chan error
}

func newKeySender() *keySender {
	s := &keySender{jobs: make(chan keyJob, 512), errs: make(chan error, 1)}
	go s.loop()
	return s
}

func (s *keySender) send(j keyJob) { s.jobs <- j }

func (s *keySender) loop() {
	var next *keyJob
	for {
		var j keyJob
		if next != nil {
			j, next = *next, nil
		} else {
			j = <-s.jobs
		}
	merge:
		for j.literal != "" {
			select {
			case n := <-s.jobs:
				if n.literal != "" && n.session == j.session {
					j.literal += n.literal
					continue
				}
				next = &n
				break merge
			default:
				break merge
			}
		}
		var err error
		switch {
		case j.paste != "":
			err = tmux.Paste(j.session, j.paste)
		case j.literal != "":
			err = tmux.SendLiteral(j.session, j.literal)
		default:
			err = tmux.SendKey(j.session, j.key)
		}
		if err != nil {
			select {
			case s.errs <- err:
			default:
			}
		}
	}
}

// liveTickMsg carries the live-mode session it was started for, so a loop left
// over from an earlier session stops.
type liveTickMsg struct{ gen int }

func liveTick(gen int) tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return liveTickMsg{gen} })
}

// syncFocus tells the engine which card you're attached to or typing into
// in live mode.
func (m *Model) syncFocus() {
	want := m.attached
	if m.mode == modeLive {
		if t := m.current(); t != nil {
			want = t.ID
		}
	}
	if want != m.focused {
		m.focused = want
		m.eng.SetFocused(want)
	}
}

func (m Model) startLive() (Model, tea.Cmd) {
	t := m.current()
	if t == nil {
		return m, nil
	}
	if _, ok := m.liveSession(t.ID); !ok {
		m.setFlash("No running agent to type into. Press r to start or resume it.")
		return m, nil
	}
	m.mode = modeLive
	m.liveGen++
	if m.prefs.HidePreview {
		m.prefs.HidePreview = false
		m.savePrefs()
	}
	return m, tea.Batch(liveTick(m.liveGen), m.capturePreview())
}

func (m Model) updateLive(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == liveExitKey {
		m.mode = modeBoard
		return m, nil
	}
	t := m.current()
	if t == nil {
		return m, nil
	}
	name, ok := m.liveSession(t.ID)
	if !ok {
		m.setFlash("No running agent here. Ctrl-q returns to the board.")
		return m, nil
	}
	job, ok := keyJobFor(name, msg)
	if !ok {
		return m, nil
	}
	m.keys.send(job)
	m.previewScroll = 0
	return m, afterKey(m.capturePreview())
}
