package inbox

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func project(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return filepath.Join(t.TempDir(), "repo")
}

func titled(s string) Request { return Request{Op: OpAdd, Title: &s} }

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestNextClaimsRequestsOldestFirst(t *testing.T) {
	root := project(t)
	for _, title := range []string{"one", "two", "three"} {
		if _, err := Send(root, titled(title)); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"one", "two", "three"} {
		p, ok, err := Next(root)
		if err != nil || !ok || p.Err != nil || *p.Request.Title != want {
			t.Fatalf("Next = %+v, %v, %v; want %q", p, ok, err, want)
		}
		Finish(root, p.ID)
	}
	if _, ok, _ := Next(root); ok {
		t.Error("a request was handed out twice")
	}
}

func TestUnfinishedClaimIsKeptAndRecoveredAfterABoardStops(t *testing.T) {
	root := project(t)
	Send(root, titled("one"))
	Send(root, titled("two"))
	first, _, _ := Next(root)

	if !exists(path(root, first.ID, claimedExt)) {
		t.Fatal("claim dropped before the request was applied")
	}
	Recover(root)
	var got []string
	for {
		p, ok, _ := Next(root)
		if !ok {
			break
		}
		got = append(got, *p.Request.Title)
		Finish(root, p.ID)
	}
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("after recovery got %v", got)
	}
}

func TestNextReportsAnUnreadableRequest(t *testing.T) {
	root := project(t)
	os.MkdirAll(dir(root), 0o755)
	os.WriteFile(path(root, "1", requestExt), []byte("{nope"), 0o644)
	p, ok, err := Next(root)
	if err != nil || !ok || p.Err == nil || p.ID != "1" {
		t.Fatalf("Next = %+v, %v, %v", p, ok, err)
	}
	Finish(root, p.ID)
	if _, ok, _ := Next(root); ok {
		t.Error("unreadable request kept")
	}
}

func TestNextWithNoInboxIsEmpty(t *testing.T) {
	if _, ok, err := Next(project(t)); ok || err != nil {
		t.Errorf("Next = %v, %v", ok, err)
	}
}

func TestNextIgnoresHalfWrittenRequests(t *testing.T) {
	root := project(t)
	os.MkdirAll(dir(root), 0o755)
	os.WriteFile(path(root, "1", requestExt)+".tmp", []byte("{"), 0o644)
	if _, ok, _ := Next(root); ok {
		t.Error("claimed a temp file")
	}
}

func TestReplyReachesTheWaiter(t *testing.T) {
	root := project(t)
	id, err := Send(root, Request{Op: OpRemove, Number: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := Wait(root, id, 100*time.Millisecond, nil); ok {
		t.Fatal("got an answer before the board gave one")
	}
	Reply(root, id, Result{Number: 3, Error: "boom"})
	res, ok, err := Wait(root, id, time.Second, nil)
	if err != nil || !ok || res.Number != 3 || res.Error != "boom" {
		t.Fatalf("Wait = %+v, %v, %v", res, ok, err)
	}
	if _, ok, _ := Wait(root, id, 0, nil); ok {
		t.Error("result kept after it was collected")
	}
}

func TestWaitStopsWhenTheBoardIsGone(t *testing.T) {
	root := project(t)
	start := time.Now()
	_, ok, err := Wait(root, "nobody", time.Minute, func() bool { return true })
	if ok || err != nil || time.Since(start) > 5*time.Second {
		t.Errorf("Wait = %v, %v after %s", ok, err, time.Since(start))
	}
}

func TestNextDropsOnlyStaleResultsAndTempFiles(t *testing.T) {
	root := project(t)
	os.MkdirAll(dir(root), 0o755)
	Reply(root, "old", Result{})
	Reply(root, "new", Result{})
	os.WriteFile(path(root, "old", ".json.tmp"), nil, 0o644)
	past := time.Now().Add(-2 * staleAfter)
	for _, f := range []string{path(root, "old", resultExt), path(root, "old", ".json.tmp")} {
		os.Chtimes(f, past, past)
	}
	Next(root)
	if exists(path(root, "old", resultExt)) || exists(path(root, "old", ".json.tmp")) {
		t.Error("stale file kept")
	}
	if !exists(path(root, "new", resultExt)) {
		t.Error("fresh result dropped")
	}
}

func TestHoldMarksTheBoardRunning(t *testing.T) {
	root := project(t)
	if Running(root) {
		t.Fatal("running before anything holds the lock")
	}
	release, ok := Hold(root)
	if !ok {
		t.Fatal("could not hold")
	}
	if !Running(root) {
		t.Error("not running while held")
	}
	if _, second := Hold(root); second {
		t.Error("a second board held the lock")
	}
	release()
	if Running(root) {
		t.Error("still running after release")
	}
}

func TestHoldOutlastsAMomentaryProbe(t *testing.T) {
	root := project(t)
	os.MkdirAll(filepath.Dir(lockPath(root)), 0o755)
	probe, err := os.OpenFile(lockPath(root), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	syscall.Flock(int(probe.Fd()), syscall.LOCK_SH)
	go func() {
		time.Sleep(60 * time.Millisecond)
		probe.Close()
	}()
	release, ok := Hold(root)
	if !ok {
		t.Fatal("a probe made the board give up the lock")
	}
	release()
}
