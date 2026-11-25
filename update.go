package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/aapzu/drover/internal/update"
)

// updateClient is replaced in tests.
var updateClient = &update.Client{}

// cmdUpdate installs the newest release over this binary. With --check it only
// says whether there is one.
func cmdUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	check := fs.Bool("check", false, "only report whether a newer release exists")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return errors.New("usage: drover update [--check]")
	}
	current := buildVersion()
	if !update.Releasable(current) {
		return fmt.Errorf("this is a development build (%s); install a release to update", current)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	latest, err := updateClient.Latest(ctx)
	if err != nil {
		return fmt.Errorf("looking for a newer release: %w", err)
	}
	if !update.Newer(current, latest) {
		fmt.Printf("drover %s is up to date\n", current)
		return nil
	}
	if *check {
		fmt.Printf("drover %s is available (you have %s); run drover update to install it\n", latest, current)
		return nil
	}
	exe, err := update.Executable()
	if err != nil {
		return err
	}
	if err := updateClient.Install(ctx, latest, exe); err != nil {
		return err
	}
	fmt.Printf("Updated drover from %s to %s. Restart the board to use it.\n", current, latest)
	return nil
}
