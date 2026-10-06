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

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
)

// loginTimeout bounds a login: a code to arrive and be typed, a phone to accept one.
const loginTimeout = 15 * time.Minute

// runLogin logs an account in through the daemon, starting it if need be: `kith login
// [network] [account]`, Matrix when no network is named. The network leads: this
// draws each step it takes (asks, reading secrets without echo; shows a code; writes
// a new account into the config) until it says it is done. Nothing typed reaches
// argv or the disk but what the config keeps.
func runLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	profile := fs.String("profile", "", "which [[profile]] the daemon runs as (default: the first one)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse login flags: %w", err)
	}
	if fs.NArg() > 2 {
		return errors.New("usage: kith login [network [account]]")
	}
	path, cfg, ready, err := loadConfig(*configPath, *profile)
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
	at, err := reachForLogin(ctx, *configPath, *profile)
	if err != nil || at.backend == nil {
		return err
	}
	defer at.backend.Stop()
	if at.note != "" {
		fmt.Fprintln(os.Stderr, "kith:", at.note)
	}
	// The daemon reads the config only when asked: an account just added to the file
	// is one it has not heard of yet.
	if err = at.backend.ReloadConfig(ctx); err != nil {
		return fmt.Errorf("have kithd re-read the config: %w", err)
	}
	account, err := loginAccount(ctx, at.backend, network, fs.Arg(1))
	if err != nil {
		return err
	}
	if network == "matrix" && account == "" && cfg.HasMatrix() {
		account = cfg.User
	}
	l := cliLogin{backend: at.backend, in: bufio.NewReader(os.Stdin), path: path}
	return l.run(ctx, network, account)
}

// loginAccount is the account named, or the network's only one; "" (a new one) when
// it has none.
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
	path string
}

// run leads the login through to its end.
func (l cliLogin) run(ctx context.Context, network, account string) error {
	step, err := l.backend.BeginLogin(ctx, network, account)
	for err == nil {
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

// ask reads each field, its help first: a secret without echo, a suggestion kept by
// an empty answer.
func (l cliLogin) ask(fields []api.LoginField) (map[string]string, error) {
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

// configTries is how often a write is made again on a config that changed under it.
const configTries = 3

// write has the daemon, the config file's writer, put a new account into it. A file
// changed meanwhile (a window saving a setting) is read again and the account put into
// that.
func (l cliLogin) write(ctx context.Context, rec api.LoginRecord) error {
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

// readLine reads one line typed after prompt, as typed (it is not a secret).
func readLine(in *bufio.Reader, prompt string) (string, error) {
	fmt.Fprintf(os.Stderr, "%s: ", prompt)
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read %s: %w", prompt, err)
	}
	return strings.TrimSpace(line), nil
}
