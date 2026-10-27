package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/qrtext"
	"golang.org/x/term"
)

// loginTimeout bounds a login: a code to arrive and be typed, a phone to accept one.
const loginTimeout = 15 * time.Minute

// runLogin is `kith login [network] [account]` (Matrix by default), through the daemon,
// starting it if needed. The network leads; this draws each step (secrets read without
// echo, codes shown, new accounts written to the config). Nothing typed reaches argv
// or disk except what the config keeps.
func runLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	byCode := fs.Bool("code", false, "log in with a code instead of scanning a QR code, where a network offers both "+
		"(WhatsApp: a code typed on the phone; Telegram: a code Telegram sends)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse login flags: %w", err)
	}
	if fs.NArg() > 2 {
		return errors.New("usage: kith login [--code] [network [account]]")
	}
	path, _, ready, err := loadConfig(*configPath)
	if err != nil || !ready {
		return err
	}
	network := "matrix"
	if fs.NArg() > 0 {
		network = strings.ToLower(fs.Arg(0))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()
	at, err := reachForLogin(ctx, *configPath)
	if err != nil || at.backend == nil {
		return err
	}
	defer at.backend.Stop()
	if at.note != "" {
		fmt.Fprintln(os.Stderr, "kith:", at.note)
	}
	// The daemon reads the config only when asked, so it has not seen a just-added account.
	if err = at.backend.ReloadConfig(ctx); err != nil {
		return fmt.Errorf("have kithd re-read the config: %w", err)
	}
	account, err := loginAccount(ctx, at.backend, network, fs.Arg(1))
	if err != nil {
		return err
	}
	l := &cliLogin{backend: at.backend, in: bufio.NewReader(os.Stdin), out: os.Stdout, path: path, byCode: *byCode}
	return l.run(ctx, network, account)
}

// loginAccount is the account named, or the network's only one; "" (new) when none.
func loginAccount(ctx context.Context, logins api.Logins, network, named string) (string, error) {
	networks, err := logins.LoginNetworks(ctx)
	if err != nil {
		return "", fmt.Errorf("ask kithd which networks it logs in to: %w", err)
	}
	i := slices.IndexFunc(networks, func(n api.LoginNetwork) bool { return n.Network == network })
	if i < 0 {
		var known []string
		for _, n := range networks {
			known = append(known, n.Network)
		}
		return "", fmt.Errorf("kith login: no network is called %s (%s)", network, strings.Join(known, ", "))
	}
	accounts := networks[i].Accounts
	switch {
	case named != "":
		if !slices.ContainsFunc(accounts, func(a api.LoginAccount) bool { return a.Name == named }) {
			return "", fmt.Errorf("kith login: %s has no account called %s", networks[i].Label, named)
		}
		return named, nil
	case len(accounts) == 1:
		return accounts[0].Name, nil
	case len(accounts) > 1:
		var names []string
		for _, a := range accounts {
			names = append(names, a.Name)
		}
		return "", fmt.Errorf("kith login %s: say which account, one of %s", network, strings.Join(names, ", "))
	}
	return "", nil
}

// cliLogin draws a login's steps on the terminal.
type cliLogin struct {
	backend interface {
		api.Logins
		GetConfig(ctx context.Context) (config.Snapshot, error)
		UpdateConfig(ctx context.Context, base string, cfg config.Config) (string, error)
	}
	in   *bufio.Reader
	out  *os.File
	path string
	// byCode takes the code a QR step offers instead (--code).
	byCode bool
	// drawn is how many terminal rows the QR code on screen takes, to draw the next
	// over it; 0 when none is on screen.
	drawn int
}

func (l *cliLogin) run(ctx context.Context, network, account string) error {
	step, err := l.backend.BeginLogin(ctx, network, account)
	for err == nil {
		if step.QR != "" {
			if err = l.qrStep(ctx, step, network); err == nil {
				step, err = l.backend.AnswerLogin(ctx, step.Login, nil)
			}
			continue
		}
		l.drawn = 0
		if step.Note != "" {
			fmt.Println(step.Note)
		}
		switch {
		case step.Restart:
			if step.Done != "" {
				fmt.Println("kith: " + step.Done + ".")
			}
			fmt.Println("kith: kithd must restart to go on (`systemctl --user restart kithd`, or stop it and run kith); " +
				"then run `kith login " + network + " " + step.Account + "` again.")
			return nil
		case step.Done != "":
			fmt.Println("kith: " + step.Done + ".")
			if g, ok := l.backend.(groupings); ok && step.Account != "" {
				return askImport(ctx, g, l.in, network, step.Account, true) // offered, once
			}
			return nil
		case step.Code != "":
			fmt.Printf("\n    %s\n\n", step.Code)
			step, err = l.backend.AnswerLogin(ctx, step.Login, nil)
		case step.Configure != nil:
			if err = l.write(ctx, *step.Configure); err == nil {
				step, err = l.backend.AnswerLogin(ctx, step.Login, nil)
			}
		default:
			var values map[string]string
			if values, err = l.ask(step.Ask); err == nil {
				step, err = l.backend.AnswerLogin(ctx, step.Login, values)
			}
		}
	}
	return fmt.Errorf("kith login %s: %w", network, err)
}

// qrStep draws a QR step, or with --code takes the other way it offers.
func (l *cliLogin) qrStep(ctx context.Context, step api.LoginStep, network string) error {
	if step.Instead == "" || !l.byCode {
		return l.drawQR(step, network)
	}
	if err := l.backend.InsteadLogin(ctx, step.Login); err != nil {
		return fmt.Errorf("%s: %w", step.Instead, err)
	}
	return nil
}

// ask reads each field after its help: secrets without echo; an empty answer keeps a
// suggestion.
func (l *cliLogin) ask(fields []api.LoginField) (map[string]string, error) {
	values := make(map[string]string, len(fields))
	for _, f := range fields {
		if f.Help != "" {
			fmt.Printf("\n%s\n\n", f.Help)
		}
		label := f.Label
		if f.Value != "" {
			label += " [" + f.Value + "]"
		}
		var value string
		var err error
		if f.Secret {
			value, err = readSecretOrEmpty(label)
		} else {
			value, err = readLine(l.in, label)
		}
		if err != nil {
			return nil, err
		}
		if value == "" {
			value = f.Value
		}
		values[f.Key] = value
	}
	return values, nil
}

// configTries is how often a write is retried on a config that changed under it.
const configTries = 3

// write has the daemon put a new account into the config; a file changed meanwhile is
// re-read and the account put into that.
func (l *cliLogin) write(ctx context.Context, rec api.LoginRecord) error {
	for try := 1; ; try++ {
		snap, err := l.backend.GetConfig(ctx)
		if err != nil {
			return fmt.Errorf("read the config: %w", err)
		}
		cfg := snap.Config
		if werr := cfg.Write(rec.Table, rec.Values); werr != nil {
			return fmt.Errorf("set the account up: %w", werr)
		}
		_, err = l.backend.UpdateConfig(ctx, snap.Revision, cfg)
		if errors.Is(err, api.ErrConfigMoved) && try < configTries {
			continue
		}
		if err != nil {
			return fmt.Errorf("save the config: %w", err)
		}
		fmt.Println("kith: wrote the account into " + l.path + ".")
		return nil
	}
}

// readLine reads one line typed after prompt, echoed (it is not a secret).
func readLine(in *bufio.Reader, prompt string) (string, error) {
	fmt.Fprintf(os.Stderr, "%s: ", prompt)
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read %s: %w", prompt, err)
	}
	return strings.TrimSpace(line), nil
}

// printQR prints data as a QR code, light on dark whatever the terminal's colors.
func printQR(data string) {
	for _, line := range qrBlock(data) {
		fmt.Println(line.text)
	}
}

// shownLine is a line to print and how many columns of it show.
type shownLine struct {
	text  string
	shown int
}

// qrBlock is data as a QR code between blank lines, indented, light on dark.
func qrBlock(data string) []shownLine {
	lines, err := qrtext.Lines(data)
	if err != nil {
		text := "    the QR code could not be drawn: " + err.Error()
		return []shownLine{{}, {text, utf8.RuneCountInString(text)}, {}}
	}
	out := []shownLine{{}}
	for _, line := range lines {
		out = append(out, shownLine{"    \x1b[97;40m" + line + "\x1b[0m", 4 + utf8.RuneCountInString(line)})
	}
	return append(out, shownLine{})
}

// drawQR draws a QR step: its note, the code, and how to take the other way it
// offers. On a terminal, a code that replaces the last (they expire) is drawn over it.
func (l *cliLogin) drawQR(step api.LoginStep, network string) error {
	block := qrBlock(step.QR)
	if step.Note != "" {
		block = append([]shownLine{{step.Note, utf8.RuneCountInString(step.Note)}}, block...)
	}
	if step.Instead != "" {
		hint := fmt.Sprintf("Can't scan it? `kith login --code %s %s` will %s.", network, step.Account, step.Instead)
		block = append(block, shownLine{hint, utf8.RuneCountInString(hint)})
	}
	fd := int(l.out.Fd())
	cols, _, err := term.GetSize(fd)
	onTerminal := term.IsTerminal(fd) && err == nil && cols > 0
	var b strings.Builder
	if onTerminal && l.drawn > 0 {
		fmt.Fprintf(&b, "\x1b[%dF\x1b[J", l.drawn) // back to where the last began; clear below
	}
	l.drawn = 0
	for _, line := range block {
		b.WriteString(line.text + "\n")
		l.drawn += max(1, (line.shown+cols-1)/max(cols, 1)) // rows, wrapped
	}
	if !onTerminal {
		l.drawn = 0 // not drawn over: each follows the last
	}
	if _, err := l.out.WriteString(b.String()); err != nil {
		return fmt.Errorf("draw the QR code: %w", err)
	}
	return nil
}
