package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/tui"
)

// runExport is `kith export [--since WHEN] [--last N] [folder]`: every room the cache
// holds, each to its own Markdown file, as /export writes one.
func runExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	since := fs.String("since", "", "only messages from `when` on: 2h, 7d, yesterday, 2026-09-18")
	last := fs.Int("last", 0, "only each room's last `n` messages")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kith export [--since WHEN] [--last N] [folder] — every room to its own Markdown file,\n"+
			"in folder, or in a dated folder under [storage] export_dir")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args) // ExitOnError
	if fs.NArg() > 1 {
		fs.Usage()
		return errors.New("one folder at most")
	}
	span, err := exportSpan(*since, *last, time.Now())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	at, err := reachForLogin(ctx, *configPath)
	if err != nil || at.backend == nil {
		return err
	}
	defer at.backend.Stop()
	dir := fs.Arg(0)
	if dir == "" {
		dir = filepath.Join(at.storage.ExportDir, "kith "+time.Now().Format("2006-01-02"))
	}
	rooms, messages := 0, 0
	err = tui.ExportAll(ctx, at.backend, at.cfg, span, dir, func(e tui.Exported) {
		rooms, messages = rooms+1, messages+e.Count
		fmt.Printf("%6d  %s\n", e.Count, e.Path)
	})
	fmt.Printf("exported %d messages from %d rooms to %s\n", messages, rooms, dir)
	if err != nil {
		return fmt.Errorf("some rooms were not exported: %w", err)
	}
	return nil
}

// exportSpan is what --since and --last ask for: both apply, the date first.
func exportSpan(since string, last int, now time.Time) (domain.SummarySpan, error) {
	if last < 0 {
		return domain.SummarySpan{}, fmt.Errorf("--last %d is not a number of messages", last)
	}
	span, err := domain.ParseSummarySpan(since, now)
	if err != nil {
		return domain.SummarySpan{}, fmt.Errorf("--since: %w", err)
	}
	if span.Count > 0 {
		return domain.SummarySpan{}, fmt.Errorf("--since %s is a number: use --last for a number of messages", since)
	}
	// The file says how many messages it holds; "since" is for a date alone.
	span.Count = last
	return span, nil
}
