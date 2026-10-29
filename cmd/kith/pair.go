package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/keystore"
	"github.com/EugeneShtoka/kith/internal/pairing"
	"github.com/EugeneShtoka/kith/internal/setup"
)

const joinUsage = "usage: kith join [--config path] [--name name] 'kith://pair?…'"

// runPair is `kith pair`: on a machine this config's kithd trusts, a pairing for a new
// device, shown as a QR code and a kith://pair URI, then the device that answers it,
// confirmed here by its digits.
func runPair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse pair flags: %w", err)
	}
	name := strings.Join(fs.Args(), " ")
	// No deadline: the pairing ends itself after ten minutes.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	at, err := reachForLogin(ctx, *configPath)
	if err != nil || at.backend == nil {
		return err
	}
	defer at.backend.Stop()
	return pair(ctx, at.backend, name, bufio.NewReader(os.Stdin), os.Stdout)
}

// pair runs one pairing against b, asking in for the confirmation.
func pair(ctx context.Context, b *daemon.Remote, name string, in *bufio.Reader, out io.Writer) error {
	var token string
	outcome, err := b.StartPairing(ctx, name, func(t domain.PairingTicket) {
		token = t.Token
		showTicket(out, t)
	}, func(p domain.PendingDevice) {
		// Asked aside, so the pairing ending (it expires, the device leaves) ends the wait.
		go func() {
			_, _ = fmt.Fprintf(out, "\n%s (%s) asks to pair.\nIt shows %s. Pair it, if it shows the same? [y/N] ",
				cmpLabel(p.Name, "A device"), p.ID, p.Digits)
			line, _ := in.ReadString('\n') // nothing read is a no
			if _, cerr := b.ConfirmPairing(ctx, token, isYes(line)); cerr != nil {
				_, _ = fmt.Fprintln(out, "kith:", cerr)
			}
		}()
	})
	if err != nil {
		return fmt.Errorf("pair: %w", err)
	}
	if outcome == domain.PairingConfirmed {
		_, _ = fmt.Fprintln(out, "Paired. It may now do everything this machine may, over [remote] listen.")
		return nil
	}
	return outcomeErr(outcome)
}

// showTicket prints what the new device joins with: a QR code, and the same as a URI.
func showTicket(out io.Writer, t domain.PairingTicket) {
	uri, err := ticketURI(t)
	if err != nil {
		_, _ = fmt.Fprintln(out, "kith: the bridge's pairing does not read:", err)
		return
	}
	var b strings.Builder
	b.WriteString("On the new device, scan this, or run `kith join` with the line below it.\n")
	for _, line := range qrBlock(uri) {
		b.WriteString(line.text + "\n")
	}
	fmt.Fprintf(&b, "kith join '%s'\n\nGood once, until %s. Waiting for the device…\n", uri, t.Expires.Local().Format("15:04"))
	_, _ = io.WriteString(out, b.String())
}

// ticketURI is a pairing's ticket as kith join reads it.
func ticketURI(t domain.PairingTicket) (string, error) {
	id, err := devicekey.ParseID(t.ID)
	if err != nil {
		return "", fmt.Errorf("its ID: %w", err)
	}
	token, err := pairing.ParseToken(t.Token)
	if err != nil {
		return "", fmt.Errorf("its token: %w", err)
	}
	return pairing.Invite{Addresses: t.Addresses, ID: id, Token: token, Expires: t.Expires}.URI(), nil
}

// isYes reads a y/N answer: only yes is yes.
func isYes(line string) bool {
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func cmpLabel(name, fallback string) string {
	if name == "" {
		return fallback
	}
	return fmt.Sprintf("%q", name)
}

// outcomeErr says why a pairing ended with nothing paired.
func outcomeErr(o domain.PairingOutcome) error {
	switch o {
	case domain.PairingConfirmed:
		return nil
	case domain.PairingRefused:
		return errors.New("the pairing was refused; nothing was paired")
	case domain.PairingExpired:
		return errors.New("the pairing expired unconfirmed; nothing was paired (`kith pair` makes another)")
	case domain.PairingAbandoned:
		return errors.New("the other side went away before it was confirmed; nothing was paired")
	}
	return fmt.Errorf("the pairing ended unconfirmed (%d)", o)
}

// runJoin is `kith join <uri>`: on a new machine, answer a pairing `kith pair` showed,
// over TLS with this machine's device key and the bridge's pinned, and once it is
// confirmed there, write the bridge into this device's device.toml.
func runJoin(args []string) error {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	name := fs.String("name", "", "how the bridge lists this device (default: this machine's hostname)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse join flags: %w", err)
	}
	if fs.NArg() != 1 {
		return errors.New(joinUsage)
	}
	inv, err := pairing.ParseURI(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("join: %w", err)
	}
	if *name == "" {
		*name, _ = os.Hostname() // none leaves the name to the bridge's side
	}
	// A new machine has no config of its own, and gets none: its kithd is the bridge's.
	path, err := configFile(*configPath)
	if err != nil {
		return err
	}
	storage, allowFile, err := setup.ClientStorage(path)
	if err != nil {
		return err
	}
	key, err := keystore.LoadOrCreateDeviceKey(keystore.DeviceStore(storage, "client-device"), allowFile)
	if err != nil {
		return fmt.Errorf("this machine's device key: %w", err)
	}
	cert, err := devicekey.Certificate(key)
	if err != nil {
		return fmt.Errorf("this machine's device key: %w", err)
	}
	self, err := devicekey.IDOf(key.Public())
	if err != nil {
		return fmt.Errorf("this machine's device key: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	b := daemon.NewBridgeRemote(cert, inv.ID, inv.Addresses)
	defer b.Stop()
	if err := join(ctx, b, inv, self, *name, os.Stdout); err != nil {
		return err
	}
	if err := rememberBridge(config.DevicePath(path), inv); err != nil {
		return fmt.Errorf("join: paired, but %w", err)
	}
	fmt.Printf("Paired. The bridge is in %s.\n", config.DevicePath(path))
	return nil
}

// join answers inv's pairing over b as the device self, listed as name, and returns
// once it is confirmed, or why not.
func join(ctx context.Context, b *daemon.Remote, inv pairing.Invite, self devicekey.ID, name string, out io.Writer) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	want := pairing.Digits(inv.ID, self, inv.Token)
	errDigits := errors.New("the bridge shows other digits than this device worked out; nothing was paired")
	outcome, err := b.Pair(ctx, inv.Token.String(), name, func(got string) {
		if got != want {
			cancel(errDigits) // ends the pairing unconfirmed
			return
		}
		_, _ = fmt.Fprintf(out, "This device shows %s. Confirm on the other device that it shows the same.\n", want)
	})
	if cause := context.Cause(ctx); errors.Is(cause, errDigits) {
		return fmt.Errorf("join: %w", cause)
	}
	if err != nil {
		return fmt.Errorf("join: %w", err)
	}
	if outcome != domain.PairingConfirmed {
		return fmt.Errorf("join: %w", outcomeErr(outcome))
	}
	return nil
}

// rememberBridge writes inv's bridge into the device file at path: over the entry with
// its ID (paired again, keeping the name given it), else as a new one, named by its
// first address's host.
func rememberBridge(path string, inv pairing.Invite) error {
	was, err := config.LoadDevice(path)
	if err != nil {
		return fmt.Errorf("the device file: %w", err)
	}
	now := was.Clone()
	host, _, _ := net.SplitHostPort(inv.Addresses[0]) // ParseURI checked it
	entry := config.Bridge{Name: host, Addresses: inv.Addresses, ID: inv.ID.String()}
	i := slices.IndexFunc(now.Bridges, func(b config.Bridge) bool {
		id, perr := devicekey.ParseID(b.ID)
		return perr == nil && id == inv.ID
	})
	if i >= 0 {
		entry.Name = now.Bridges[i].Name
		now.Bridges[i] = entry
	} else {
		now.Bridges = append(now.Bridges, entry)
	}
	if err := setup.ValidateDevice(now); err != nil {
		return fmt.Errorf("the device file: %w", err)
	}
	if _, err := config.UpdateDevice(path, was, now); err != nil {
		return fmt.Errorf("write the device file: %w", err)
	}
	return nil
}
