package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/inbox"
	"github.com/aapzu/drover/internal/linear"
	"github.com/aapzu/drover/internal/store"
)

const cliUsage = `Change the board from outside it:
  drover list [--json]                      list the cards
  drover add <title> [-p prompt] [-c column]
  drover add --linear <key or url> [-c column]
  drover move <number> <column>
  drover edit <number> [--title t] [--prompt p]
  drover rm <number> [--force]

Cards are addressed by the number list shows. Columns are ids or names. A
prompt of - is read from stdin. --force lets rm discard a dirty worktree.
Changes go through the open board, or are applied the next time it opens.
`

// waitForBoard is how long a command waits for the open board to answer.
const waitForBoard = 2 * time.Minute

var cliCommands = map[string]func(root string, args []string) error{
	"list": cmdList,
	"add":  cmdAdd,
	"move": cmdMove,
	"edit": cmdEdit,
	"rm":   cmdRemove,
}

// parseArgs parses flags wherever they appear among args and returns the
// positional ones.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return rest, nil
		}
		rest = append(rest, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func parseNumber(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(s, "#"))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%q is not a card number", s)
	}
	return n, nil
}

// send queues req for the board and prints confirm's line once the board has
// applied it. Without an open board the change waits until the next one.
func send(root string, req inbox.Request, confirm func(inbox.Result) string) error {
	if err := validate(root, req); err != nil {
		return err
	}
	id, err := inbox.Send(root, req)
	if err != nil {
		return err
	}
	if !inbox.Running(root) {
		fmt.Println("Queued: drover isn't open for this project, so it is applied when it opens")
		return nil
	}
	res, ok, err := inbox.Wait(root, id, waitForBoard, func() bool { return !inbox.Running(root) })
	switch {
	case err != nil:
		return err
	case !ok && !inbox.Running(root):
		return errors.New("the board closed before answering; the change is applied when it opens again")
	case !ok:
		return fmt.Errorf("no answer from the board after %s; the change may or may not have been applied, so run drover list before retrying", waitForBoard)
	case res.Error != "":
		return errors.New(res.Error)
	}
	fmt.Println(confirm(res))
	return nil
}

// validate rejects an unknown column, card number or Linear reference up front.
func validate(root string, req inbox.Request) error {
	board, _, err := config.Load(root)
	if err != nil {
		return err
	}
	if req.Column != "" {
		if _, ok := board.Find(req.Column); !ok {
			return fmt.Errorf("unknown column %q; columns are %s", req.Column, columnNames(board))
		}
	}
	if req.Linear != "" {
		if _, ok := linear.ParseKey(req.Linear); !ok {
			return fmt.Errorf("%q is not a Linear issue key or URL", req.Linear)
		}
	}
	if req.Number == 0 {
		return nil
	}
	state, err := store.Load(root)
	if err != nil {
		return err
	}
	if state.ByNumber(req.Number) == nil {
		return fmt.Errorf("no card #%d", req.Number)
	}
	return nil
}

func columnNames(b *config.Board) string {
	names := make([]string, len(b.Columns))
	for i, c := range b.Columns {
		names[i] = c.ID
	}
	return strings.Join(names, ", ")
}

// card is one entry of list's output.
type card struct {
	Number   int    `json:"number"`
	Title    string `json:"title"`
	Prompt   string `json:"prompt,omitempty"`
	Column   string `json:"column"`
	ColumnID string `json:"columnId"`
	Issue    string `json:"issue,omitempty"`
	PR       string `json:"pr,omitempty"`
	order    int
}

func cmdList(root string, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	board, _, err := config.Load(root)
	if err != nil {
		return err
	}
	state, err := store.Load(root)
	if err != nil {
		return err
	}
	cards := make([]card, 0, len(state.Tasks))
	for _, t := range state.Tasks {
		col, idx, ok := board.Column(t.ColumnID)
		if !ok {
			col, idx = config.Column{ID: t.ColumnID, Name: t.ColumnID}, len(board.Columns)
		}
		c := card{Number: t.Number, Title: t.Title, Prompt: t.Prompt, Column: col.Name, ColumnID: col.ID, order: idx}
		if t.Issue != nil {
			c.Issue = t.Issue.Key
		}
		if t.PR != nil {
			c.PR = t.PR.URL
		}
		cards = append(cards, c)
	}
	slices.SortFunc(cards, func(a, b card) int {
		if a.order != b.order {
			return a.order - b.order
		}
		return a.Number - b.Number
	})
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(cards)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, c := range cards {
		title := c.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(w, "#%d\t%s\t%s\t%s\n", c.Number, c.Column, title, strings.TrimSpace(c.Issue+" "+c.PR))
	}
	return w.Flush()
}

func cmdAdd(root string, args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	var prompt, column, issue string
	fs.StringVar(&prompt, "p", "", "")
	fs.StringVar(&prompt, "prompt", "", "")
	fs.StringVar(&column, "c", "", "")
	fs.StringVar(&column, "column", "", "")
	fs.StringVar(&issue, "linear", "", "")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	req := inbox.Request{Op: inbox.OpAdd, Column: column, Linear: issue}
	switch {
	case issue != "" && (len(rest) > 0 || prompt != ""):
		return errors.New("--linear takes the title and prompt from the issue")
	case issue == "" && (len(rest) != 1 || strings.TrimSpace(rest[0]) == ""):
		return errors.New("usage: drover add <title> [-p prompt] [-c column]")
	}
	if len(rest) == 1 {
		req.Title = &rest[0]
	}
	if prompt, err = promptArg(prompt); err != nil {
		return err
	}
	if prompt != "" {
		req.Prompt = &prompt
	}
	return send(root, req, func(r inbox.Result) string {
		return fmt.Sprintf("Added #%d %s (%s)", r.Number, r.Title, r.Column)
	})
}

// promptArg returns s, or what stdin holds when s is -.
func promptArg(s string) (string, error) {
	if s != "-" {
		return s, nil
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", errors.New("the prompt on stdin is empty")
	}
	return string(data), nil
}

func cmdMove(root string, args []string) error {
	fs := flag.NewFlagSet("move", flag.ContinueOnError)
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 2 {
		return errors.New("usage: drover move <number> <column>")
	}
	n, err := parseNumber(rest[0])
	if err != nil {
		return err
	}
	return send(root, inbox.Request{Op: inbox.OpMove, Number: n, Column: rest[1]}, func(r inbox.Result) string {
		return fmt.Sprintf("Moved #%d to %s", r.Number, r.Column)
	})
}

func cmdEdit(root string, args []string) error {
	fs := flag.NewFlagSet("edit", flag.ContinueOnError)
	title := fs.String("title", "", "")
	prompt := fs.String("prompt", "", "")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: drover edit <number> [--title t] [--prompt p]")
	}
	n, err := parseNumber(rest[0])
	if err != nil {
		return err
	}
	req := inbox.Request{Op: inbox.OpEdit, Number: n}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "title":
			req.Title = title
		case "prompt":
			req.Prompt = prompt
		}
	})
	if req.Title == nil && req.Prompt == nil {
		return errors.New("nothing to change: pass --title or --prompt")
	}
	if req.Title != nil && strings.TrimSpace(*req.Title) == "" {
		return errors.New("a card needs a title")
	}
	if req.Prompt != nil {
		if *req.Prompt, err = promptArg(*req.Prompt); err != nil {
			return err
		}
	}
	return send(root, req, func(r inbox.Result) string { return fmt.Sprintf("Updated #%d", r.Number) })
}

func cmdRemove(root string, args []string) error {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	force := fs.Bool("force", false, "")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: drover rm <number> [--force]")
	}
	n, err := parseNumber(rest[0])
	if err != nil {
		return err
	}
	return send(root, inbox.Request{Op: inbox.OpRemove, Number: n, Force: *force}, func(r inbox.Result) string {
		return fmt.Sprintf("Deleted #%d", r.Number)
	})
}
