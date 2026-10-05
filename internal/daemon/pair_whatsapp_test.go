package daemon_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/daemon"
)

// pairer hands out a code, then links, or fails as told.
type pairer struct {
	account string
	fail    error
}

func (p *pairer) PairWhatsApp(_ context.Context, account string, code func(string) error) (string, error) {
	p.account = account
	if err := code("ABCD-EFGH"); err != nil {
		return "", err
	}
	if p.fail != nil {
		return "", p.fail
	}
	return "whatsapp:44000000001@s.whatsapp.net", nil
}

// servePairing is a daemon whose WhatsApp link is link (nil: WhatsApp off).
func servePairing(t *testing.T, link api.WhatsAppLink) *daemon.Remote {
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
			Backend: apitest.Nop{}, Streams: daemon.NewStreams(), State: daemon.NewState(), WhatsApp: link,
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Errorf("Serve() = %v", err)
		}
	})
	return daemon.NewRemote(path)
}

// `kith login whatsapp` gets the code while the daemon waits on the phone, then the
// linked account.
func TestPairingCarriesTheCodeThenTheAccount(t *testing.T) {
	t.Parallel()
	p := &pairer{}
	remote := servePairing(t, p)
	var codes []string
	linked, err := remote.PairWhatsApp(context.Background(), "home", func(code string) error {
		codes = append(codes, code)
		return nil
	})
	if err != nil {
		t.Fatalf("PairWhatsApp: %v", err)
	}
	if p.account != "home" || len(codes) != 1 || codes[0] != "ABCD-EFGH" || linked != "whatsapp:44000000001@s.whatsapp.net" {
		t.Errorf("account %q, codes %v, linked %q", p.account, codes, linked)
	}
}

// A refusal from the phone, and WhatsApp being off, reach the command as such.
func TestPairingFailuresCrossTheSocket(t *testing.T) {
	t.Parallel()
	refused := errors.New("the phone refused")
	if _, err := servePairing(t, &pairer{fail: refused}).PairWhatsApp(context.Background(), "home",
		func(string) error { return nil }); err == nil {
		t.Error("a refusal came back as success")
	}
	if _, err := servePairing(t, nil).PairWhatsApp(context.Background(), "home",
		func(string) error { t.Error("a code with WhatsApp off"); return nil }); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("WhatsApp off = %v, want ErrNetworkOff", err)
	}
}
