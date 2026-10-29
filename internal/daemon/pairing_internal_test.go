package daemon

import (
	"net"
	"slices"
	"testing"
)

// A wildcard listener is dialed at each of this machine's addresses another device can
// reach: IPv4 first, and IPv6 too for [::]; never loopback or link-local. Any other
// address is dialed as bound.
func TestAWildcardIsDialedAtEveryReachableAddress(t *testing.T) {
	t.Parallel()
	ifaces := []net.Addr{
		&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
		&net.IPNet{IP: net.ParseIP("::1"), Mask: net.CIDRMask(128, 128)},
		&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)},
		&net.IPNet{IP: net.ParseIP("169.254.3.4"), Mask: net.CIDRMask(16, 32)},
		&net.IPNet{IP: net.ParseIP("fd00::5"), Mask: net.CIDRMask(64, 128)},
		&net.IPNet{IP: net.ParseIP("192.168.1.5").To4(), Mask: net.CIDRMask(24, 32)},
		&net.IPAddr{IP: net.ParseIP("10.0.0.9")}, // not an interface's network: skipped
		&net.IPNet{IP: net.ParseIP("100.64.0.3"), Mask: net.CIDRMask(10, 32)},
	}
	for _, tc := range []struct {
		bound []string
		want  []string
	}{
		{[]string{"0.0.0.0:7443"}, []string{"192.168.1.5:7443", "100.64.0.3:7443"}},
		{[]string{"[::]:7443"}, []string{"192.168.1.5:7443", "100.64.0.3:7443", "[fd00::5]:7443"}},
		{[]string{"192.168.1.5:7443"}, []string{"192.168.1.5:7443"}},
		{[]string{"127.0.0.1:7443"}, []string{"127.0.0.1:7443"}},
		// Named in order, each once.
		{[]string{"100.64.0.3:7443", "0.0.0.0:7443"}, []string{"100.64.0.3:7443", "192.168.1.5:7443"}},
		{[]string{"0.0.0.0:1", "[::]:2"}, []string{"192.168.1.5:1", "100.64.0.3:1", "192.168.1.5:2", "100.64.0.3:2", "[fd00::5]:2"}},
		{nil, nil},
	} {
		if got := expandWildcards(tc.bound, ifaces); !slices.Equal(got, tc.want) {
			t.Errorf("expandWildcards(%q) = %q, want %q", tc.bound, got, tc.want)
		}
	}
	// A machine with nothing reachable has nothing to offer.
	if got := expandWildcards([]string{"0.0.0.0:7443"}, ifaces[:4]); len(got) != 0 {
		t.Errorf("only loopback and link-local: %q, want none", got)
	}
}
