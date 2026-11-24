package engine

import (
	"strings"
	"testing"

	"github.com/aapzu/drover/internal/store"
)

const moveBoard = `{"columns":[
  {"id":"plan","name":"Planning"},
  {"id":"exec","name":"Executing","autoSpawn":true},
  {"id":"review","name":"Review","automations":{"onEnter":[{"name":"Ping","type":"notify","title":"{{title}}","body":"in {{toColumn}}"}]}},
  {"id":"done","name":"Done","role":"done"}]}`

func TestMoveRefusesWhatCannotHappenAndIgnoresTheSameColumn(t *testing.T) {
	e := boardEngine(t, moveBoard)
	card := withQueued(agentCard("a", 1, "exec", 0))
	e.state.Tasks = []*store.Task{card}

	if err := e.Move("missing", "review", MoveOptions{}); err == nil || !strings.Contains(err.Error(), "task not found") {
		t.Errorf("moving an unknown card = %v", err)
	}
	if err := e.Move("a", "nowhere", MoveOptions{}); err == nil || !strings.Contains(err.Error(), "unknown column nowhere") {
		t.Errorf("moving to an unknown column = %v", err)
	}
	if err := e.Move("a", "exec", MoveOptions{}); err != nil {
		t.Errorf("moving to the column it is in = %v", err)
	}
	if len(card.Log) != 0 || card.Visit != 0 || card.Sessions[store.MainSession].Status != store.StatusRunning {
		t.Errorf("a refused or empty move changed the card: visit %d, log %v", card.Visit, card.Log)
	}
}

func TestMoveToAColumnWithoutAnAgentSuspendsIt(t *testing.T) {
	e := boardEngine(t, moveBoard)
	card := withQueued(agentCard("a", 1, "exec", 0))
	e.state.Tasks = []*store.Task{card}

	if err := e.Move("a", "review", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	s := card.Sessions[store.MainSession]
	if card.ColumnID != "review" || s.Status != store.StatusSuspended || s.Pending != nil || card.Visit != 1 || !hasLog(card, "Moved Executing → Review") {
		t.Errorf("column %q, session %s with queue %v, visit %d, log %v", card.ColumnID, s.Status, s.Pending, card.Visit, card.Log)
	}
}

func TestMoveRunsTheColumnsEnterAutomationsUnlessSkipped(t *testing.T) {
	for name, skip := range map[string]bool{"entered normally": false, "entered with skipEnter": true} {
		e := boardEngine(t, moveBoard)
		card := withQueued(agentCard("a", 1, "plan", 0))
		card.Title = "Enter " + name
		e.state.Tasks = []*store.Task{card}

		if err := e.Move("a", "review", MoveOptions{skipEnter: skip}); err != nil {
			t.Fatal(err)
		}
		if got := desktopNotified("Enter " + name + ":"); (len(got) == 1) == skip {
			t.Errorf("%s: notifications = %q", name, got)
		}
	}
}

func TestMovingTheDrivingCardHandsTheAgentToAnotherCardInAnAgentColumn(t *testing.T) {
	e := boardEngine(t, moveBoard)
	owner := withQueued(agentCard("o", 1, "exec", 0))
	sibling := &store.Task{ID: "s", Number: 2, GroupID: "o", ColumnID: "exec"}
	e.state.Tasks = []*store.Task{owner, sibling}

	if err := e.Move("o", "review", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if owner.ActiveCard != "s" || owner.Sessions[store.MainSession].Status != store.StatusRunning {
		t.Errorf("active card %q, session %s; the agent should carry on for the sibling", owner.ActiveCard, owner.Sessions[store.MainSession].Status)
	}

	if err := e.Move("s", "review", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if status := owner.Sessions[store.MainSession].Status; status != store.StatusSuspended {
		t.Errorf("session %s after the last agent card left", status)
	}
}

func TestMovingACardThatDoesNotDriveLeavesTheAgentToTheDriver(t *testing.T) {
	e := boardEngine(t, moveBoard)
	owner := withQueued(agentCard("o", 1, "exec", 0))
	owner.ActiveCard = "o"
	sibling := &store.Task{ID: "s", Number: 2, GroupID: "o", ColumnID: "exec"}
	e.state.Tasks = []*store.Task{owner, sibling}

	if err := e.Move("s", "done", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if owner.ActiveCard != "o" || owner.Sessions[store.MainSession].Status != store.StatusRunning || sibling.ColumnID != "done" {
		t.Errorf("active card %q, session %s, sibling in %q", owner.ActiveCard, owner.Sessions[store.MainSession].Status, sibling.ColumnID)
	}
}
