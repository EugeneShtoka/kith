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
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// telegramLoginTimeout bounds a login: the code to arrive and be typed, and the
// password after it.
const telegramLoginTimeout = 10 * time.Minute

// runTelegramLogin logs one [[telegram.account]] in: the app to log in through (one's
// own, or kith's), then the daemon has Telegram send a code, which is typed here, and
// the two-step verification password when the account has one. The daemon keeps the
// session in the keyring and connects.
func runTelegramLogin(args []string) error {
	fs := flag.NewFlagSet("login telegram", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	profile := fs.String("profile", "", "which [[profile]] the daemon runs as (default: the first one)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse login flags: %w", err)
	}
	if fs.NArg() > 1 {
		return errors.New("usage: kith login telegram [account name]")
	}
	_, cfg, ready, err := loadConfig(*configPath, *profile)
	if err != nil || !ready {
		return err
	}
	if len(cfg.Telegram.Accounts) == 0 {
		return errors.New("add a [[telegram.account]] (name, phone) to the config first, or run :login telegram in kith")
	}
	account, err := setup.TelegramAccount(cfg.Telegram, fs.Arg(0))
	if err != nil {
		return fmt.Errorf("kith login telegram: %w", err)
	}
	in := bufio.NewReader(os.Stdin)
	fmt.Printf("Logging in to Telegram %s (%s).\n\n%s\nLeft empty, kith's own app is used, when this build has one.\n\n",
		account.Name, account.Phone, setup.TelegramAppSteps)
	app, err := readTelegramApp(in)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, telegramLoginTimeout)
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
	sent, err := at.backend.SendTelegramCode(ctx, account.Name, app)
	if err != nil {
		return fmt.Errorf("log in to Telegram %s: %w", account.Name, err)
	}
	fmt.Printf("Telegram sent a code to %s.\n", sent.Via)
	return answerTelegramLogin(ctx, at.backend, in, account.Name)
}

// answerTelegramLogin reads the code, and the password when the account has one, until
// the login takes them; a wrong one is read again.
func answerTelegramLogin(ctx context.Context, login api.TelegramLogin, in *bufio.Reader, name string) error {
	var code, password string
	for {
		var err error
		if code == "" {
			if code, err = readLine(in, "Code"); err != nil {
				return err
			}
		}
		signed, err := login.SignInTelegram(ctx, name, code, password)
		switch {
		case err == nil:
			fmt.Printf("kith: logged in to Telegram as %s; its chats appear in kith now.\n", signed.Name)
			return nil
		case errors.Is(err, api.ErrBadCode):
			fmt.Fprintln(os.Stderr, "That is not the code Telegram sent; try again.")
			code = ""
		case errors.Is(err, api.ErrBadPassword), errors.Is(err, api.ErrPasswordNeeded):
			if errors.Is(err, api.ErrBadPassword) {
				fmt.Fprintln(os.Stderr, "That is not the password; try again.")
			}
			if password, err = readSecret("Two-step verification password"); err != nil {
				return err
			}
		default:
			return fmt.Errorf("log in to Telegram %s: %w", name, err)
		}
	}
}

// readTelegramApp reads an app's api_id and api_hash; both empty is kith's own.
func readTelegramApp(in *bufio.Reader) (api.TelegramApp, error) {
	raw, err := readLine(in, "api_id (empty: kith's own)")
	if err != nil || raw == "" {
		return api.TelegramApp{}, err
	}
	id, err := setup.TelegramAppID(raw)
	if err != nil {
		return api.TelegramApp{}, err
	}
	hash, err := readSecret("api_hash")
	if err != nil {
		return api.TelegramApp{}, err
	}
	if err := setup.CheckTelegramAppHash(hash); err != nil {
		return api.TelegramApp{}, err
	}
	return api.TelegramApp{ID: id, Hash: hash}, nil
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
