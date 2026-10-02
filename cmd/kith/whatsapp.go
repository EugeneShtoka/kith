package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/EugeneShtoka/kith/internal/setup"
)

// pairingTimeout is how long a pairing code may wait to be typed on the phone.
const pairingTimeout = 10 * time.Minute

// runWhatsAppLogin links one [[whatsapp.account]] to kith: the daemon (which owns the
// WhatsApp store) asks WhatsApp for a pairing code, this prints it, and the phone's
// answer comes back.
func runWhatsAppLogin(args []string) error {
	fs := flag.NewFlagSet("login whatsapp", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	profile := fs.String("profile", "", "which [[profile]] the daemon runs as (default: the first one)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse login flags: %w", err)
	}
	if fs.NArg() > 1 {
		return errors.New("usage: kith login whatsapp [account name]")
	}
	_, cfg, ready, err := loadConfig(*configPath, *profile)
	if err != nil || !ready {
		return err
	}
	if !cfg.WhatsApp.Enabled {
		return errors.New("set `enabled = true` under [whatsapp] in the config, and restart kithd, first " +
			"(turning WhatsApp on takes a restart; adding an account to it does not)")
	}
	account, err := setup.WhatsAppAccount(cfg.WhatsApp, fs.Arg(0))
	if err != nil {
		return fmt.Errorf("kith login whatsapp: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, pairingTimeout)
	defer cancel()
	at, err := attach(ctx, *configPath, *profile, readyTimeout)
	if err != nil || at.backend == nil {
		return err
	}
	backend := at.backend
	defer backend.Stop()
	if at.note != "" {
		fmt.Fprintln(os.Stderr, "kith:", at.note)
	}

	// The daemon reads the config only when asked: an account just added to the file
	// is one it has not heard of yet.
	if err = backend.ReloadConfig(ctx); err != nil {
		return fmt.Errorf("have kithd re-read the config: %w", err)
	}
	fmt.Printf("Linking WhatsApp %s (%s)…\n", account.Name, account.Phone)
	linked, err := backend.PairWhatsApp(ctx, account.Name, func(code string) error {
		fmt.Printf("\n    %s\n\n", code)
		fmt.Println("On the phone: WhatsApp → Settings → Linked devices → Link a device →")
		fmt.Println("“Link with phone number instead”, and type the code above.")
		return nil
	})
	if err != nil {
		return fmt.Errorf("link WhatsApp %s: %w", account.Name, err)
	}
	fmt.Printf("kith: linked WhatsApp %s as %s; its groups appear in kith now.\n", account.Name, linked)
	return nil
}
