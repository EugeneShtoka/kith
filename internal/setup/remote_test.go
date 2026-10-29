package setup_test

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

func TestOnlyTheAddressesWrittenAreListenedOn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		listen []string
		ok     bool
		says   string
	}{
		{nil, true, ""},
		{[]string{"192.168.1.5:7443", "100.64.0.3:7443"}, true, ""},
		{[]string{"[fd00::5]:7443"}, true, ""},
		{[]string{"localhost:7443"}, true, ""},
		// Every interface, but only written as such.
		{[]string{"0.0.0.0:7443"}, true, ""},
		{[]string{"[::]:7443"}, true, ""},
		{[]string{":7443"}, false, "0.0.0.0:7443"},
		{[]string{"[]:7443"}, false, "names no host"},
		{[]string{"192.168.1.5"}, false, "host:port"},
		{[]string{"192.168.1.5:"}, false, "port"},
		{[]string{"192.168.1.5:0"}, false, "port"},
		{[]string{"192.168.1.5:65536"}, false, "port"},
		{[]string{"192.168.1.5:https"}, false, "port"},
		{[]string{"fd00::5:7443"}, false, "host:port"},
		{[]string{""}, false, "host:port"},
		{[]string{"192.168.1.5:7443", "192.168.1.5:7443"}, false, "twice"},
	} {
		err := setup.RemoteListen(config.Remote{Listen: tc.listen})
		if (err == nil) != tc.ok || (err != nil && !strings.Contains(err.Error(), tc.says)) {
			t.Errorf("RemoteListen(%q) = %v, want ok %v saying %q", tc.listen, err, tc.ok, tc.says)
		}
		// The config's own check runs it.
		cfg := config.Config{
			Profiles: []config.Profile{{Name: "all", Include: []string{"*"}}},
			Remote:   config.Remote{Listen: tc.listen},
		}
		if verr := setup.Validate(cfg); (verr == nil) != tc.ok {
			t.Errorf("Validate with listen %q = %v, want ok %v", tc.listen, verr, tc.ok)
		}
	}
}
