package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aapzu/drover/internal/engine"
	"github.com/aapzu/drover/internal/store"
)

type fakeEngine struct {
	err   error
	moves []engine.MoveOptions
	snap  engine.Snapshot
}

func (f *fakeEngine) Move(id, targetID string, opts engine.MoveOptions) error {
	f.moves = append(f.moves, opts)
	return f.err
}

func (f *fakeEngine) Snapshot() engine.Snapshot { return f.snap }

func fakeFor(m Model, err error) *fakeEngine {
	snap := m.snap
	snap.At = time.Now()
	return &fakeEngine{err: err, snap: snap}
}

func key(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestMoveShowsCardInTargetColumnAtOnce(t *testing.T) {
	m := testModel(120, 40, 3, 1)
	card := m.snap.Tasks[0]
	m.moveCmd(card, m.columns()[2])

	if got := m.tasksIn("c0"); len(got) != 0 {
		t.Errorf("source column still lists %d cards", len(got))
	}
	if got := m.tasksIn("c2"); len(got) != 2 || got[1].ID != card.ID {
		t.Errorf("target column should end with the moving card, got %v", got)
	}
	if got := m.status(card); !strings.Contains(got, "moving") {
		t.Errorf("status = %q, want a moving label", got)
	}
}

func TestMoveResultCarriesWhatTheHandlerNeeds(t *testing.T) {
	m := testModel(120, 40, 3, 1)
	eng := fakeFor(m, nil)
	cmd := moveTo(eng, m.busy, "c0-t0", 1, m.columns()[2], false)

	msg := cmd().(actionDoneMsg)
	if msg.id != "c0-t0" || !msg.at.Equal(m.busy["c0-t0"].at) {
		t.Errorf("msg id/at = %q/%v, want the busy entry's", msg.id, msg.at)
	}
	if !msg.snap.At.Equal(eng.snap.At) {
		t.Error("msg should carry the snapshot taken after the move")
	}
	if msg.done != "Moved #1 to Col 2" || msg.retryDone != nil {
		t.Errorf("done = %q retryDone set = %v", msg.done, msg.retryDone != nil)
	}

	next, _ := m.Update(msg)
	m = next.(Model)
	if len(m.busy) != 0 || m.flash != "Moved #1 to Col 2" {
		t.Errorf("busy = %v flash = %q after a successful move", m.busy, m.flash)
	}
}

func TestFailedMoveReturnsCard(t *testing.T) {
	m := testModel(120, 40, 3, 1)
	card := m.snap.Tasks[0]
	cmd := moveTo(fakeFor(m, errors.New("boom")), m.busy, card.ID, card.Number, m.columns()[2], false)

	next, _ := m.Update(cmd())
	m = next.(Model)

	if got := m.tasksIn("c0"); len(got) != 1 || got[0].ID != card.ID {
		t.Errorf("card should be back in its column, got %v", got)
	}
	if len(m.busy) != 0 {
		t.Errorf("busy = %v, want cleared", m.busy)
	}
	if !strings.Contains(m.flash, "boom") {
		t.Errorf("flash = %q, want the error", m.flash)
	}
}

func TestDirtyMoveAsksThenRetriesWithForce(t *testing.T) {
	m := testModel(120, 40, 3, 1)
	card := m.snap.Tasks[0]
	eng := fakeFor(m, engine.ErrDirty)
	msg := moveTo(eng, m.busy, card.ID, card.Number, m.columns()[2], false)().(actionDoneMsg)
	if msg.retryDone == nil || msg.done != "" {
		t.Fatalf("an ErrDirty move should offer a retry only, got done=%q retry=%v", msg.done, msg.retryDone != nil)
	}

	next, _ := m.Update(msg)
	m = next.(Model)
	if m.mode != modeConfirm {
		t.Fatalf("mode = %v, want confirm", m.mode)
	}
	if len(m.busy) != 0 {
		t.Errorf("card should wait in its column while confirming, busy=%v", m.busy)
	}

	eng.err = nil
	retry := m.confirmAction()
	if m.busy[card.ID].column != "c2" {
		t.Errorf("retry should show the card moving again, busy=%v", m.busy)
	}
	retry()
	if got := eng.moves; len(got) != 2 || got[0].Force || !got[1].Force {
		t.Errorf("moves = %+v, want a plain move then a forced one", got)
	}
}

func TestCardRunsOneOpAtATime(t *testing.T) {
	m := testModel(120, 40, 3, 1)
	card := m.snap.Tasks[0]
	eng := fakeFor(m, nil)
	moveTo(eng, m.busy, card.ID, card.Number, m.columns()[2], false)
	first := m.busy[card.ID]

	cmd := moveTo(eng, m.busy, card.ID, card.Number, m.columns()[1], false)
	if m.busy[card.ID] != first {
		t.Errorf("a second op replaced the first: %v", m.busy[card.ID])
	}
	next, _ := m.Update(cmd())
	m = next.(Model)
	if len(eng.moves) != 0 {
		t.Errorf("the rejected move still ran: %v", eng.moves)
	}
	if !strings.Contains(m.flash, "busy") || m.busy[card.ID] != first {
		t.Errorf("flash = %q busy = %v, want the busy error and the first op left alone", m.flash, m.busy)
	}
}

func TestSlowActionDoesNotClearNewerOne(t *testing.T) {
	m := testModel(120, 40, 3, 1)
	card := m.snap.Tasks[0]
	m.moveCmd(card, m.columns()[2])
	first := m.busy[card.ID].at
	m.busy[card.ID] = op{label: "deleting", at: first.Add(time.Second)}

	next, _ := m.Update(actionDoneMsg{id: card.ID, at: first, snap: m.snap})
	m = next.(Model)
	if m.busy[card.ID].label != "deleting" {
		t.Errorf("finishing the older action cleared the newer one: busy=%v", m.busy)
	}
}

func TestActionsLabelTheirCardWithoutMovingIt(t *testing.T) {
	for _, tc := range []struct{ keys, label string }{
		{"S", "suspending"},
		{"r", "starting agent"},
		{"xy", "deleting"},
	} {
		m := testModel(120, 40, 2, 1)
		card := m.current()
		var model tea.Model = m
		for _, k := range tc.keys {
			model, _ = model.Update(key(string(k)))
		}
		m = model.(Model)
		o, ok := m.busy[card.ID]
		if !ok || o.label != tc.label || o.column != "" {
			t.Errorf("keys %q: busy[%s] = %+v, want label %q and no column", tc.keys, card.ID, o, tc.label)
		}
	}
}

func TestEnterStartsAgentWithoutOne(t *testing.T) {
	m := testModel(120, 40, 2, 1)
	card := m.current()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if o, ok := m.busy[card.ID]; !ok || o.label != "starting agent" {
		t.Errorf("busy[%s] = %+v, want starting agent", card.ID, o)
	}
}

func TestStartedAgentDoesNotAttachOverModal(t *testing.T) {
	m := testModel(120, 40, 2, 1)
	card := m.current()
	m.snap.Live = map[string]string{card.ID: "drover-test"}
	m.mode, m.modalTask = modeSend, card.ID
	if _, cmd := m.Update(actionDoneMsg{id: card.ID, snap: m.snap, attach: true}); cmd != nil {
		t.Error("attached over an open modal")
	}
}

func TestStatusPrefersEngineStep(t *testing.T) {
	m := testModel(120, 40, 1, 1)
	card := m.snap.Tasks[0]
	if got := m.status(card); got != "" {
		t.Errorf("idle card status = %q, want none", got)
	}
	m.busy[card.ID] = op{label: "moving"}
	if got := m.status(card); !strings.Contains(got, "moving") {
		t.Errorf("status = %q, want the board's label", got)
	}
	m.snap.Busy = map[string]string{card.ID: "creating worktree"}
	if got := m.status(card); !strings.Contains(got, "creating worktree") {
		t.Errorf("status = %q, want the engine's step", got)
	}
}

func TestOrphanCardMovesOutOfFirstColumn(t *testing.T) {
	m := testModel(120, 40, 3, 1)
	orphan := &store.Task{ID: "orphan", Number: 9, Title: "lost", ColumnID: "deleted"}
	m.snap.Tasks = append(m.snap.Tasks, orphan)

	count := func(col int) (n int) {
		for _, c := range m.tasksForColumn(col) {
			if c.ID == orphan.ID {
				n++
			}
		}
		return n
	}
	if count(0) != 1 {
		t.Fatalf("orphan should show once in the first column, got %d", count(0))
	}

	moveTo(fakeFor(m, nil), m.busy, orphan.ID, orphan.Number, m.columns()[2], false)
	if count(0) != 0 || count(2) != 1 {
		t.Errorf("a moving orphan should leave column 0 and show once in column 2, got %d and %d", count(0), count(2))
	}
}

func TestStaleSnapshotIsIgnored(t *testing.T) {
	m := testModel(120, 40, 2, 1)
	now := time.Now()
	m.snap.At = now
	stale := m.snap
	stale.At = now.Add(-time.Second)
	stale.Tasks = nil

	next, _ := m.Update(snapMsg(stale))
	if got := len(next.(Model).snap.Tasks); got != 2 {
		t.Errorf("an older snapshot replaced the newer one: %d tasks left", got)
	}

	fresh := m.snap
	fresh.At = now.Add(time.Second)
	fresh.Tasks = fresh.Tasks[:1]
	next, _ = next.Update(snapMsg(fresh))
	if got := len(next.(Model).snap.Tasks); got != 1 {
		t.Errorf("a newer snapshot was not applied: %d tasks", got)
	}
}

func TestApplySnapKeepsCursorAndModalValid(t *testing.T) {
	m := testModel(120, 40, 3, 1)
	m.col = 2
	m.mode = modeMove
	m.modalTask = "c2-t0"

	smaller := testModel(120, 40, 2, 1).snap
	m.applySnap(smaller)
	if m.col != 1 {
		t.Errorf("col = %d, want clamped to the last column", m.col)
	}
	if m.mode != modeBoard {
		t.Errorf("mode = %v, want the modal closed once its card is gone", m.mode)
	}
}

func TestSnapshotRequestsDoNotPileUp(t *testing.T) {
	m := testModel(120, 40, 1, 1)
	m.keys = &keySender{errs: make(chan error, 1)}
	m.prefs.HidePreview = true
	next, cmd := m.Update(tickMsg{})
	m = next.(Model)
	if cmd == nil || !m.snapBusy {
		t.Fatal("first tick should request a snapshot")
	}
	next, _ = m.Update(tickMsg{})
	m = next.(Model)
	if !m.snapBusy {
		t.Fatal("snapBusy should stay set until the snapshot arrives")
	}
	next, _ = m.Update(snapMsg(m.snap))
	if next.(Model).snapBusy {
		t.Error("snapMsg should clear snapBusy")
	}
}

func TestLiveSessionComesFromTheSnapshot(t *testing.T) {
	m := testModel(120, 40, 1, 1)
	m.snap.Live = map[string]string{"c0-t0": "repo-1"}
	if name, ok := m.liveSession("c0-t0"); !ok || name != "repo-1" {
		t.Errorf("liveSession = %q, %v", name, ok)
	}
	if _, ok := m.liveSession("other"); ok {
		t.Error("an unknown card has no live session")
	}
}
