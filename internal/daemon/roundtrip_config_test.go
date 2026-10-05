package daemon_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
)

// A config a client means to write reaches the daemon's check whole, as the client
// holds it, and the check's refusal comes back in its own words; a daemon with no
// check refuses the call rather than passing everything.
func TestCheckConfigRoundTrip(t *testing.T) {
	t.Parallel()
	var got config.Config
	refuse := errors.New(`whatsapp.account[0] (home): phone "+1" is not an international number`)
	check := func(_ context.Context, cfg config.Config) error {
		got = cfg
		if len(cfg.WhatsApp.Accounts) > 1 {
			return refuse
		}
		return nil
	}
	client := serveCheck(t, check)

	var sent config.Config
	sent.Display.FPS = 30
	sent.WhatsApp.Accounts = []config.WhatsAppAccount{{Name: "home", Phone: "+44 7700 900001"}}
	sent.Keys.FillDefaults()
	if err := client.CheckConfig(context.Background(), sent); err != nil {
		t.Fatalf("CheckConfig = %v", err)
	}
	if !reflect.DeepEqual(got, sent) {
		t.Errorf("the daemon checked %+v, want %+v", got.WhatsApp, sent.WhatsApp)
	}
	sent.WhatsApp.Accounts = append(sent.WhatsApp.Accounts, config.WhatsAppAccount{Name: "work", Phone: "+1"})
	if err := client.CheckConfig(context.Background(), sent); err == nil || !strings.Contains(err.Error(), refuse.Error()) {
		t.Errorf("CheckConfig = %v, want the check's refusal", err)
	}

	if err := serveCheck(t, nil).CheckConfig(context.Background(), sent); err == nil {
		t.Error("a daemon with no check passed a config")
	}
}

// serveCheck serves a daemon with check, and is a client of it.
func serveCheck(t *testing.T, check daemon.CheckConfig) *daemon.Remote {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kithd.sock")
	ln, err := daemon.Listen(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- daemon.Serve(ctx, ln, &daemon.Daemon{
			Backend: &fakeBackend{Nop: nopWithChannels()}, Streams: daemon.NewStreams(), State: daemon.NewState(),
			CheckConfig: check,
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Errorf("Serve = %v", err)
		}
	})
	return daemon.NewRemote(path)
}
