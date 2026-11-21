package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/keystore"
	"github.com/EugeneShtoka/kith/internal/setup"
)

const devicesUsage = "usage: kith devices [add <id> [name] | remove <id|name> | id | recovery-key | rotate [--bridge]]"

// runDevices is `kith devices`: the devices paired with this bridge, and this bridge's
// own ID (list), pairing one by its pasted ID (add), unpairing one (remove), this
// machine's own ID as a client, to pair with another bridge (id), the recovery key
// of this machine's kithd (recovery-key), or a new key for this machine as a client of
// its bridge, or with --bridge for its kithd (rotate).
func runDevices(args []string) error {
	fs := flag.NewFlagSet("devices", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse devices flags: %w", err)
	}
	verb, rest := "", fs.Args()
	if len(rest) > 0 {
		verb, rest = rest[0], rest[1:]
	}
	switch {
	case verb == "id" && len(rest) == 0:
		return printClientID(*configPath)
	case verb == "recovery-key" && len(rest) == 0:
		return printBridgeRecoveryKey(*configPath, os.Stdout)
	case verb == "" || verb == "list" && len(rest) == 0:
		return withDevices(*configPath, listDevices)
	case verb == "add" && len(rest) >= 1:
		return withDevices(*configPath, func(ctx context.Context, b *daemon.Remote) error {
			d, err := b.AddDevice(ctx, rest[0], strings.Join(rest[1:], " "))
			if err != nil {
				return fmt.Errorf("pair %s: %w", rest[0], err)
			}
			fmt.Printf("Paired %s. It may now do everything this machine may, over [remote] listen.\n", deviceLabel(d))
			return nil
		})
	case verb == "rotate" && len(rest) == 0:
		return rotateDevice(*configPath)
	case verb == "rotate" && slices.Equal(rest, []string{"--bridge"}):
		return withDevices(*configPath, func(ctx context.Context, b *daemon.Remote) error { return rotateBridge(ctx, b, *configPath) })
	case verb == "remove" && len(rest) == 1:
		return withDevices(*configPath, func(ctx context.Context, b *daemon.Remote) error { return removeDevice(ctx, b, rest[0]) })
	}
	return errors.New(devicesUsage)
}

// withDevices runs do against this config's kithd.
func withDevices(configPath string, do func(context.Context, *daemon.Remote) error) error {
	ctx, cancel := reachIgnoreCtx()
	defer cancel()
	at, err := reachForLogin(ctx, configPath)
	if err != nil || at.backend == nil {
		return err
	}
	defer at.backend.Stop()
	return do(ctx, at.backend)
}

// listDevices writes this bridge's ID and the devices paired with it.
func listDevices(ctx context.Context, b *daemon.Remote) error {
	self, devices, err := b.Devices(ctx)
	if err != nil {
		return fmt.Errorf("list the paired devices: %w", err)
	}
	fmt.Printf("This bridge: %s\n", self)
	if len(devices) == 0 {
		fmt.Println("No devices are paired. On a device, `kith devices id` prints its ID; here, `kith devices add <id> [name]` pairs it.")
		return nil
	}
	names := map[string]string{self: "this bridge"}
	for _, d := range devices {
		names[d.ID] = deviceLabel(d)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	// The tabwriter only buffers: Flush reports a failed write.
	_, _ = fmt.Fprintln(tw, "NAME\tROLE\tID\tADDED\tBY")
	for _, d := range devices {
		by, ok := names[d.AddedBy]
		if !ok {
			by = d.AddedBy // a device since unpaired
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", d.Name, d.Role, d.ID, d.AddedAt.Local().Format("2006-01-02 15:04"), by)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("list the paired devices: %w", err)
	}
	return nil
}

// removeDevice unpairs the device arg names: its ID, else its name.
func removeDevice(ctx context.Context, b *daemon.Remote, arg string) error {
	id := arg
	if _, err := devicekey.ParseID(arg); err != nil {
		_, devices, lerr := b.Devices(ctx)
		if lerr != nil {
			return fmt.Errorf("unpair %s: %w", arg, lerr)
		}
		id = ""
		for _, d := range devices {
			if d.Name == arg {
				id = d.ID
			}
		}
		if id == "" {
			return fmt.Errorf("unpair %s: no paired device has that name or ID (`kith devices` lists them)", arg)
		}
	}
	if err := b.RemoveDevice(ctx, id); err != nil {
		return fmt.Errorf("unpair %s: %w", arg, err)
	}
	fmt.Printf("Unpaired %s; its connections are closed.\n", arg)
	return nil
}

// deviceLabel is a device's name, or its ID when it has none.
func deviceLabel(d domain.Device) string {
	if d.Name != "" {
		return d.Name
	}
	return d.ID
}

// printClientID prints this machine's device ID as a client, made on first use, for
// pairing with a bridge elsewhere (`kith devices add` there).
func printClientID(configPath string) error {
	path, err := configFile(configPath)
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
	id, err := devicekey.IDOf(key.Public())
	if err != nil {
		return fmt.Errorf("this machine's device key: %w", err)
	}
	fmt.Println(id)
	return nil
}

// rotateDevice moves this machine, as a client of the bridge it joined, to a new device
// key: signed by the old key and the new, sent over the old, and kept once the bridge
// took it.
func rotateDevice(configPath string) error {
	path, err := configFile(configPath)
	if err != nil {
		return err
	}
	_, joined, err := setup.JoinedBridge(path)
	switch {
	case err != nil:
		return err
	case !joined:
		return errors.New("this machine has joined no bridge: `kith devices rotate --bridge` rotates the key of kithd here")
	}
	storage, allowFile, err := setup.ClientStorage(path)
	if err != nil {
		return err
	}
	st := keystore.DeviceStore(storage, "client-device")
	old, err := keystore.FindDeviceKey(st, allowFile)
	switch {
	case err != nil:
		return fmt.Errorf("this machine's device key: %w", err)
	case old == nil:
		return errors.New("this machine has no device key to rotate")
	}
	next, err := devicekey.Generate()
	if err != nil {
		return fmt.Errorf("rotate this device's key: %w", err)
	}
	rot, key, err := devicekey.Rotate(old, next)
	if err != nil {
		return fmt.Errorf("rotate this device's key: %w", err)
	}
	return withDevices(configPath, func(ctx context.Context, b *daemon.Remote) error {
		// shortcut: an answer lost after the bridge took the rotation leaves the new key
		// unkept and the old refused, so the device joins again; keep the new key before
		// asking if that happens.
		d, err := b.RotateDevice(ctx, key, rot)
		if err != nil {
			return fmt.Errorf("rotate this device's key (if the bridge took it before its answer was lost, `kith join` it again): %w", err)
		}
		if err := keystore.KeepDeviceKey(st, allowFile, next); err != nil {
			return fmt.Errorf("the bridge took the new key, but it could not be kept here, so this device must `kith join` again: %w", err)
		}
		fmt.Printf("This device's ID is now %s.\n", d.ID)
		fmt.Println("Restart kith-notify, kith-mcp and the desktop window here: until then they hold the old key, which the bridge refuses now.")
		return nil
	})
}

// rotateBridge moves the kithd of this machine to a new device key, and prints its new
// recovery key.
func rotateBridge(ctx context.Context, b *daemon.Remote, configPath string) error {
	was, id, err := b.RotateBridge(ctx)
	if err != nil {
		return fmt.Errorf("rotate the bridge's key: %w", err)
	}
	fmt.Printf("This bridge's device ID is now %s (it was %s).\n", id, was)
	fmt.Println("Devices and bridges pinning the old ID take the new one when they next connect, within 30 days; one that does not joins again.")
	fmt.Println("The old recovery key no longer restores this bridge. The new one:")
	return printBridgeRecoveryKey(configPath, os.Stdout)
}
