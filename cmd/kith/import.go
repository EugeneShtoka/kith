package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// groupings is the daemon copying an account's groupings (Telegram's folders) into tags.
type groupings interface {
	LoginNetworks(ctx context.Context) ([]api.LoginNetwork, error)
	PreviewGroupings(ctx context.Context, network, account string) ([]domain.GroupingDiff, error)
	ApplyGroupings(ctx context.Context, network, account string, choices map[string]domain.GroupingChoice) error
}

// runImport is `kith import <network> [account]`: the account's folders, copied into
// tags once, asking per tag where the copy would change one.
func runImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	profile := fs.String("profile", "", "which [[profile]] account to run as (default: the first one)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kith import <network> [account] — copy an account's folders (Telegram's) into tags, once")
	}
	_ = fs.Parse(args) // ExitOnError
	if fs.NArg() < 1 {
		fs.Usage()
		return errors.New("which network?")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	at, err := reachForLogin(ctx, *configPath, *profile)
	if err != nil || at.backend == nil {
		return err
	}
	defer at.backend.Stop()
	network := strings.ToLower(fs.Arg(0))
	account, err := importAccount(ctx, at.backend, network, fs.Arg(1))
	if err != nil {
		return err
	}
	return askImport(ctx, at.backend, bufio.NewReader(os.Stdin), network, account, false)
}

// importAccount is the account named, or the network's only one.
func importAccount(ctx context.Context, g groupings, network, named string) (string, error) {
	if named != "" {
		return named, nil
	}
	networks, err := g.LoginNetworks(ctx)
	if err != nil {
		return "", fmt.Errorf("list the networks: %w", err)
	}
	for _, n := range networks {
		if !strings.EqualFold(n.Network, network) {
			continue
		}
		switch len(n.Accounts) {
		case 0:
			return "", fmt.Errorf("no %s account is set up — run `kith login %s` first", n.Label, network)
		case 1:
			return n.Accounts[0].Name, nil
		}
		names := make([]string, 0, len(n.Accounts))
		for _, a := range n.Accounts {
			names = append(names, a.Name)
		}
		return "", fmt.Errorf("which %s account: %s?", n.Label, strings.Join(names, ", "))
	}
	return "", fmt.Errorf("no network is called %s", network)
}

// askImport previews an account's folders and copies them as answered. offered, after
// a login: one question for all (new tags made, existing ones only gaining chats), and
// nothing said when there are none; else a question per tag the copy would change.
func askImport(ctx context.Context, g groupings, in *bufio.Reader, network, account string, offered bool) error {
	diffs, err := g.PreviewGroupings(ctx, network, account)
	if offered && (err != nil || len(diffs) == 0) {
		return nil //nolint:nilerr // unasked, after a login: a network without folders says nothing
	}
	if errors.Is(err, api.ErrNotOnNetwork) {
		fmt.Println("kith: " + network + " has no folders to copy.")
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the folders: %w", err)
	}
	if len(diffs) == 0 {
		fmt.Println("kith: " + account + " has no folders.")
		return nil
	}
	choices := map[string]domain.GroupingChoice{}
	fmt.Printf("\n%s's folders:\n", account)
	for _, d := range diffs {
		fmt.Println("  " + describeFolder(d))
	}
	if offered {
		if yes(in, "\nCopy them into tags? Tags are made for new ones; existing tags only gain chats. [y/N] ") {
			for _, d := range diffs {
				choices[d.Grouping.Name] = domain.MergeIn
			}
		}
	} else {
		for _, d := range diffs {
			choices[d.Grouping.Name] = chooseFolder(in, d)
		}
	}
	copied := 0
	for _, c := range choices {
		if c != domain.KeepTags {
			copied++
		}
	}
	if copied == 0 {
		return nil
	}
	if err := g.ApplyGroupings(ctx, network, account, choices); err != nil {
		return fmt.Errorf("copy the folders: %w", err)
	}
	fmt.Printf("kith: copied %d folder(s) into tags.\n", copied)
	return nil
}

// describeFolder is one folder and what a copy would do with its tag.
func describeFolder(d domain.GroupingDiff) string {
	var line strings.Builder
	line.WriteString(d.Grouping.Name + ": ")
	switch {
	case !d.Exists:
		fmt.Fprintf(&line, "a new tag, %d chat(s)", len(d.Grouping.Rooms))
	case len(d.Add)+len(d.Remove) == 0:
		line.WriteString("the tag holds these chats already")
	default:
		fmt.Fprintf(&line, "the tag differs: +%d −%d", len(d.Add), len(d.Remove))
	}
	for _, left := range d.Grouping.Left {
		line.WriteString("\n      (" + left + ")")
	}
	return line.String()
}

// chooseFolder asks what to do with one folder's tag: a new one is made when wanted,
// one that differs taken, merged or kept.
func chooseFolder(in *bufio.Reader, d domain.GroupingDiff) domain.GroupingChoice {
	switch {
	case !d.Exists:
		if yes(in, fmt.Sprintf("Make the tag %q? [Y/n] ", d.Grouping.Name), true) {
			return domain.TakeNetworks
		}
	case len(d.Add)+len(d.Remove) > 0:
		fmt.Printf("%s: [t]ake the app's (+%d −%d), [m]erge (+%d), or [k]eep kith's? [k] ",
			d.Grouping.Name, len(d.Add), len(d.Remove), len(d.Add))
		switch answer(in) {
		case "t":
			return domain.TakeNetworks
		case "m":
			return domain.MergeIn
		}
	}
	return domain.KeepTags
}

// yes asks a yes-or-no question; empty is fallback (no, unless given).
func yes(in *bufio.Reader, question string, fallback ...bool) bool {
	fmt.Print(question)
	switch answer(in) {
	case "y", "yes":
		return true
	case "":
		return len(fallback) > 0 && fallback[0]
	}
	return false
}

// answer is one line read, trimmed and lower-cased; empty at the end of input.
func answer(in *bufio.Reader) string {
	line, _ := in.ReadString('\n')
	return strings.ToLower(strings.TrimSpace(line))
}
