// Package inbox carries board changes from the drover CLI to the board.
package inbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/aapzu/drover/internal/store"
)

const (
	OpAdd    = "add"
	OpMove   = "move"
	OpEdit   = "edit"
	OpRemove = "rm"
)

// staleAfter is how long an unclaimed result or leftover temp file is kept, for
// requests whose CLI stopped waiting.
const staleAfter = 10 * time.Minute

const (
	requestExt = ".json"
	claimedExt = ".claimed"
	resultExt  = ".result"
	tmpExt     = ".tmp"
)

// Request is one change to the board.
type Request struct {
	Op     string  `json:"op"`
	Number int     `json:"number,omitempty"`
	Title  *string `json:"title,omitempty"`
	Prompt *string `json:"prompt,omitempty"`
	Column string  `json:"column,omitempty"`
	Linear string  `json:"linear,omitempty"`
	Force  bool    `json:"force,omitempty"`
}

// Result is the board's answer to a request.
type Result struct {
	Number int    `json:"number,omitempty"`
	Title  string `json:"title,omitempty"`
	Column string `json:"column,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Pending is a request claimed from the inbox. Err is set when its file could
// not be read or parsed.
type Pending struct {
	ID      string
	Request Request
	Err     error
}

func dir(root string) string { return filepath.Join(store.Dir(root), "inbox") }

func path(root, id, ext string) string { return filepath.Join(dir(root), id+ext) }

func writeAtomic(file string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := file + tmpExt
	err = os.WriteFile(tmp, data, 0o644)
	if err == nil {
		err = os.Rename(tmp, file)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// Send queues req and returns its id. Ids sort in the order they were sent.
func Send(root string, req Request) (string, error) {
	if err := os.MkdirAll(dir(root), 0o755); err != nil {
		return "", err
	}
	id := fmt.Sprintf("%020d-%s", time.Now().UnixNano(), uuid.NewString()[:8])
	return id, writeAtomic(path(root, id, requestExt), req)
}

// Wait polls for the board's answer to id, removes it and returns it. ok is
// false when none arrived within timeout, or when stop reports the board gone.
func Wait(root, id string, timeout time.Duration, stop func() bool) (res Result, ok bool, err error) {
	file := path(root, id, resultExt)
	for deadline := time.Now().Add(timeout); ; time.Sleep(50 * time.Millisecond) {
		data, err := os.ReadFile(file)
		if err == nil {
			os.Remove(file)
			if err := json.Unmarshal(data, &res); err != nil {
				return Result{}, false, fmt.Errorf("the board's answer is unreadable: %w", err)
			}
			return res, true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return res, false, err
		}
		if time.Now().After(deadline) {
			return res, false, nil
		}
		if stop != nil && stop() {
			return res, false, nil
		}
	}
}

// Next claims the oldest queued request, or reports ok=false when there is
// none. It also drops results and temp files nobody collected.
func Next(root string) (p Pending, ok bool, err error) {
	d := dir(root)
	entries, err := os.ReadDir(d)
	if errors.Is(err, os.ErrNotExist) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	var oldest string
	for _, ent := range entries {
		name := ent.Name()
		switch {
		case strings.HasSuffix(name, resultExt), strings.HasSuffix(name, tmpExt):
			if info, err := ent.Info(); err == nil && time.Since(info.ModTime()) > staleAfter {
				os.Remove(filepath.Join(d, name))
			}
		case strings.HasSuffix(name, requestExt) && (oldest == "" || name < oldest):
			oldest = name
		}
	}
	if oldest == "" {
		return p, false, nil
	}
	p.ID = strings.TrimSuffix(oldest, requestExt)
	if err := os.Rename(path(root, p.ID, requestExt), path(root, p.ID, claimedExt)); err != nil {
		return p, false, err
	}
	data, err := os.ReadFile(path(root, p.ID, claimedExt))
	if err == nil {
		err = json.Unmarshal(data, &p.Request)
	}
	p.Err = err
	return p, true, nil
}

// Finish drops the claim on a request once it has been applied and answered.
func Finish(root, id string) { os.Remove(path(root, id, claimedExt)) }

// Recover queues again the requests a board was applying when it stopped.
func Recover(root string) {
	claimed, _ := filepath.Glob(path(root, "*", claimedExt))
	for _, file := range claimed {
		os.Rename(file, strings.TrimSuffix(file, claimedExt)+requestExt)
	}
}

// Reply writes the result for Wait to pick up.
func Reply(root, id string, res Result) error {
	return writeAtomic(path(root, id, resultExt), res)
}

func lockPath(root string) string { return filepath.Join(store.Dir(root), "board.lock") }

// Hold marks the board as open until release is called, or the process dies.
// ok is false when another board holds it.
func Hold(root string) (release func(), ok bool) {
	if err := os.MkdirAll(store.Dir(root), 0o755); err != nil {
		return nil, false
	}
	f, err := os.OpenFile(lockPath(root), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false
	}
	for range 10 {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		f.Close()
		return nil, false
	}
	return func() { f.Close() }, true
}

// Running reports whether a board is open for the project.
func Running(root string) bool {
	f, err := os.Open(lockPath(root))
	if err != nil {
		return false
	}
	defer f.Close()
	return errors.Is(syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB), syscall.EWOULDBLOCK)
}
