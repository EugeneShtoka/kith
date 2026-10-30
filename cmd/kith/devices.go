package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/keystore"
	"github.com/EugeneShtoka/kith/internal/setup"
)

const devicesUsage = "usage: kith devices [add <id> [name] | remove <id|name> | id | recovery-key]"

// runDevices is `kith devices`: the devices paired with this bridge, and this bridge's
// own ID (list), pairing one by its pasted ID (add), unpairing one (remove), this
// machine's own ID as a client, to pair with another bridge (id), or the recovery key
// of this machine's kithd (recovery-key).
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
