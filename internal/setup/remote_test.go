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

func TestABridgeEntryMustBeDialable(t *testing.T) {
	t.Parallel()
	const id = "C523-XW2U-B4T2-RKIG-PNZM-42UB-FX3Z-OLGG-UHU5-KVZ4-3YQV-KQR7-KXKA"
	other := strings.Replace(id, "C523", "D523", 1)
	good := config.Bridge{Name: "home", Addresses: []string{"192.168.1.5:7443", "[fd00::5]:7443"}, ID: id}
	with := func(edit func(*config.Bridge)) config.Bridge {
		b := good
		b.Addresses = append([]string(nil), good.Addresses...)
		edit(&b)
		return b
	}
	for _, tc := range []struct {
		name    string
		bridges []config.Bridge
		says    string
	}{
		{"none", nil, ""},
		{"one", []config.Bridge{good}, ""},
		{"an id in lower case, no dashes", []config.Bridge{with(func(b *config.Bridge) { b.ID = strings.ToLower(strings.ReplaceAll(id, "-", "")) })}, ""},
		{"two bridges", []config.Bridge{good, with(func(b *config.Bridge) { b.ID = other })}, ""},
		{"no addresses", []config.Bridge{with(func(b *config.Bridge) { b.Addresses = nil })}, "no addresses"},
		{"an address with no port", []config.Bridge{with(func(b *config.Bridge) { b.Addresses[1] = "192.168.1.6" })}, "host:port"},
		{"an address with no host", []config.Bridge{with(func(b *config.Bridge) { b.Addresses[0] = ":7443" })}, "host:port"},
		{"port 0", []config.Bridge{with(func(b *config.Bridge) { b.Addresses[0] = "h:0" })}, "port"},
		{"no id", []config.Bridge{with(func(b *config.Bridge) { b.ID = "" })}, "id"},
		{"an id that does not read", []config.Bridge{with(func(b *config.Bridge) { b.ID = id[:len(id)-1] })}, "id"},
		{"one id twice", []config.Bridge{good, with(func(b *config.Bridge) { b.Name = "again"; b.ID = strings.ToLower(id) })}, "another"},
	} {
		err := setup.Bridges(tc.bridges)
		if (err == nil) != (tc.says == "") || (err != nil && !strings.Contains(err.Error(), tc.says)) {
			t.Errorf("%s: Bridges = %v, want saying %q", tc.name, err, tc.says)
		}
		// The device file's own check runs it.
		if verr := setup.ValidateDevice(config.Device{Bridges: tc.bridges}); (verr == nil) != (tc.says == "") {
			t.Errorf("%s: ValidateDevice = %v, want ok %v", tc.name, verr, tc.says == "")
		}
	}
}
