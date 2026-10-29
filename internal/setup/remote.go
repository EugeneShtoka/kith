package setup

import (
	"fmt"
	"net"
	"slices"
	"strconv"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/pairing"
)

// RemoteListen refuses a [remote] listen entry that is no host:port with a port, one
// with no host (which would take every interface unasked: 0.0.0.0 or [::] must be
// written), and one named twice.
func RemoteListen(r config.Remote) error {
	for i, addr := range r.Listen {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return fmt.Errorf("remote.listen: %q is no host:port (write 192.168.1.5:7443, or [fd00::5]:7443)", addr)
		}
		if host == "" {
			return fmt.Errorf("remote.listen: %q names no host; write the address clients reach, or 0.0.0.0%s to listen on every interface", addr, addr)
		}
		if n, perr := strconv.Atoi(port); perr != nil || n < 1 || n > 65535 {
			return fmt.Errorf("remote.listen: %q: the port must be a number from 1 to 65535", addr)
		}
		if slices.Contains(r.Listen[:i], addr) {
			return fmt.Errorf("remote.listen: %q is named twice", addr)
		}
	}
	return nil
}

// Bridges refuses a [[bridge]] with no address, an address no device can dial, an ID
// that does not read, or an ID another entry has.
func Bridges(bridges []config.Bridge) error {
	var seen []devicekey.ID
	for _, b := range bridges {
		if len(b.Addresses) == 0 {
			return fmt.Errorf("bridge %q: no addresses; write host:port for each", b.Name)
		}
		for _, addr := range b.Addresses {
			if err := pairing.CheckAddress(addr); err != nil {
				return fmt.Errorf("bridge %q: addresses: %w", b.Name, err)
			}
		}
		id, err := devicekey.ParseID(b.ID)
		if err != nil {
			return fmt.Errorf("bridge %q: id: %w", b.Name, err)
		}
		if slices.Contains(seen, id) {
			return fmt.Errorf("bridge %q: another [[bridge]] has its id", b.Name)
		}
		seen = append(seen, id)
	}
	return nil
}
