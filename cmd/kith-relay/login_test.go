package main

import (
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"rsc.io/qr"

	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	devicev1 "github.com/EugeneShtoka/kith/internal/api/device/v1"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
)

// A login's new account is written into the shared config onto whatever other writers
// (the TUI's settings editor, another window's theme) wrote before it or at once: it
// lands, and so does each of theirs.
func TestANewAccountKeepsEveryOtherWritersChange(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	themeConfig(t, cfgPath, config.GUITheme{})
	k := serveThemeKithd(t, cfgPath)
	relay := deviceClient(relayFor(t, k.socket, cfgPath))
	kithd := backendv1connect.NewBackendServiceClient(daemon.Dial(k.socket), "http://kithd")
	for seed := range uint64(40) {
		rng := rand.New(rand.NewPCG(seed, 0x1091)) // #nosec G404 -- reproducible
		themeConfig(t, cfgPath, config.GUITheme{})
		handWrite(t, cfgPath, "")
		// Other writers of the shared config: each a different key, so each must stand.
		keys := slices.Clone(themeKeys)
		rng.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
		others := make([]themeWrite, rng.IntN(4))
		for i := range others {
			kind := []int{tuiEditor, otherWindow}[rng.IntN(2)]
			others[i] = themeWrite{kind: kind, key: keys[i], scope: devicev1.ThemeScope_THEME_SCOPE_SHARED}
			others[i].value = genThemeValue(rng, keys[i])
		}
		name := fmt.Sprintf("acct%d", seed)
		var wg sync.WaitGroup
		for _, w := range others {
			wg.Go(func() {
				if err := w.apply(t.Context(), cfgPath, kithd, relay); err != nil {
					t.Errorf("seed %d: %+v: %v", seed, w, err)
				}
			})
		}
		_, err := relay.WriteAccount(t.Context(), connect.NewRequest(&devicev1.WriteAccountRequest{
			Table: "linkedin.account", Values: map[string]string{"name": name},
		}))
		wg.Wait()
		if err != nil {
			t.Fatalf("seed %d: the account's write: %v", seed, err)
		}
		cfg, err := config.Load(cfgPath)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if !slices.ContainsFunc(cfg.LinkedIn.Accounts, func(a config.LinkedInAccount) bool { return a.Name == name }) {
			t.Fatalf("seed %d: the config's LinkedIn accounts are %+v, without %s", seed, cfg.LinkedIn.Accounts, name)
		}
		for _, w := range others {
			if got := w.valueIn(t, cfgPath); got != w.value {
				t.Fatalf("seed %d: %+v was lost: the file holds %q", seed, w, got)
			}
		}
	}
}

// A record the config has no table for is refused, and nothing is written.
func TestARecordTheConfigRefusesIsNotWritten(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	themeConfig(t, cfgPath, config.GUITheme{})
	k := serveThemeKithd(t, cfgPath)
	relay := deviceClient(relayFor(t, k.socket, cfgPath))
	before, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = relay.WriteAccount(t.Context(), connect.NewRequest(&devicev1.WriteAccountRequest{
		Table: "nowhere.account", Values: map[string]string{"name": "x"},
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a record of no table: %v, want INVALID_ARGUMENT", err)
	}
	after, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.Encode(after) != config.Encode(before) {
		t.Fatal("the config changed for a refused record")
	}
}

// A QR step's data comes back as the modules the TUI's encoder draws, row by row; no
// data is refused.
func TestAQRCodeIsTheTUIsModules(t *testing.T) {
	t.Parallel()
	relay := deviceClient(relayFor(t, kithdSocket(t), filepath.Join(t.TempDir(), "config.toml")))
	for _, data := range []string{"x", "tg://login?token=" + strings.Repeat("ab", 22), "2@abc,def,ghi,jkl"} {
		got, err := relay.QRCode(t.Context(), connect.NewRequest(&devicev1.QRCodeRequest{Data: data}))
		if err != nil {
			t.Fatalf("%q: %v", data, err)
		}
		want, err := qr.Encode(data, qr.L)
		if err != nil {
			t.Fatal(err)
		}
		dark := got.Msg.GetDark()
		if len(dark) != want.Size*want.Size {
			t.Fatalf("%q: %d modules, want %d", data, len(dark), want.Size*want.Size)
		}
		for y := range want.Size {
			for x := range want.Size {
				if (dark[y*want.Size+x] == 1) != want.Black(x, y) {
					t.Fatalf("%q: module (%d, %d) differs", data, x, y)
				}
			}
		}
	}
	if _, err := relay.QRCode(t.Context(), connect.NewRequest(&devicev1.QRCodeRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a QR code of nothing: %v, want INVALID_ARGUMENT", err)
	}
}
