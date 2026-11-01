package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/keystore"
	"github.com/EugeneShtoka/kith/internal/pairing"
	"github.com/EugeneShtoka/kith/internal/setup"
)

const joinUsage = "usage: kith join [--config path] [--name name] [--bridge] 'kith://pair?…'"

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
			as := ""
			if p.Role == domain.RoleBridge {
				as = " as a bridge: it joins the mesh, and every bridge admits its devices and it theirs"
			}
			_, _ = fmt.Fprintf(out, "\n%s (%s) asks to pair%s.\nIt shows %s. Pair it, if it shows the same? [y/N] ",
				cmpLabel(p.Name, "A device"), p.ID, as, p.Digits)
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
	return fmt.Errorf("pair: %w", daemon.PairingOutcomeErr(outcome))
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

// runJoin is `kith join <uri>`: on a new machine, answer a pairing `kith pair` showed,
// over TLS with this machine's device key and the bridge's pinned, and once it is
// confirmed there, write the bridge into this device's device.toml.
func runJoin(args []string) error {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	name := fs.String("name", "", "how the bridge lists this device (default: this machine's hostname)")
	asBridge := fs.Bool("bridge", false, "join this machine's kithd to the bridge's mesh, as another bridge")
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
	if *asBridge {
		return joinAsBridge(*configPath, fs.Arg(0), *name)
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
	if _, err := setup.RememberBridge(config.DevicePath(path), inv); err != nil {
		return fmt.Errorf("join: paired, but %w", err)
	}
	fmt.Printf("Paired. The bridge is in %s.\n", config.DevicePath(path))
	return nil
}

// join answers inv's pairing over b as the device self, listed as name, and returns
// once it is confirmed, or why not.
func join(ctx context.Context, b *daemon.Remote, inv pairing.Invite, self devicekey.ID, name string, out io.Writer) error {
	if err := b.Join(ctx, inv, self, name, func(digits string) {
		_, _ = fmt.Fprintf(out, "This device shows %s. Confirm on the other device that it shows the same.\n", digits)
	}); err != nil {
		return fmt.Errorf("join: %w", err)
	}
	return nil
}

// joinAsBridge is `kith join --bridge`: this machine's kithd, over its socket, joins
// the mesh of the bridge uri names, listed as name.
func joinAsBridge(configPath, uri, name string) error {
	// No deadline: the pairing ends itself after ten minutes.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	at, err := reachHere(ctx, configPath)
	if err != nil || at.backend == nil {
		return err
	}
	defer at.backend.Stop()
	return joinMesh(ctx, at.backend, uri, name, os.Stdout)
}

// joinMesh has b join the mesh uri names and says how it went.
func joinMesh(ctx context.Context, b *daemon.Remote, uri, name string, out io.Writer) error {
	outcome, joined, err := b.JoinMesh(ctx, uri, name, func(bringing []domain.Device) {
		if len(bringing) == 0 {
			_, _ = fmt.Fprintln(out, "This bridge brings no paired device to the mesh.")
			return
		}
		_, _ = fmt.Fprintf(out, "Once joined, every bridge of the mesh admits the %d devices paired here:\n", len(bringing))
		for _, d := range bringing {
			_, _ = fmt.Fprintf(out, "  %s (%s)\n", deviceLabel(d), d.ID)
		}
	}, func(digits string) {
		_, _ = fmt.Fprintf(out, "This bridge shows %s. Confirm on the other device that it shows the same.\n", digits)
	})
	if err != nil {
		return fmt.Errorf("join: %w", err)
	}
	if outcome != domain.PairingConfirmed {
		return fmt.Errorf("join: %w", daemon.PairingOutcomeErr(outcome))
	}
	_, _ = fmt.Fprintf(out, "Joined. This bridge now admits the mesh's %d members:\n", len(joined))
	for _, d := range joined {
		_, _ = fmt.Fprintf(out, "  %s (%s), %s\n", deviceLabel(d), d.ID, d.Role)
	}
	_, _ = fmt.Fprintln(out, "`kith mesh` shows the bridges and how their sync stands.")
	return nil
}
