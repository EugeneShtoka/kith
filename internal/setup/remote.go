package setup

import (
	"fmt"
	"net"
	"slices"
	"strconv"

	"github.com/EugeneShtoka/kith/internal/config"
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
