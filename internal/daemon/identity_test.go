package daemon_test

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/daemon"
)

// selvesBackend says who this person is.
type selvesBackend struct {
	apitest.Nop
	ids []string
}

func (b selvesBackend) Selves(context.Context) ([]string, error) { return b.ids, nil }

// serveIdentity is a daemon over b with state.
func serveIdentity(t *testing.T, b selvesBackend, state *daemon.State) *daemon.Remote {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kithd.sock")
	ln, err := daemon.Listen(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- daemon.Serve(ctx, ln, &daemon.Daemon{Backend: b, Streams: daemon.NewStreams(), State: state})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Errorf("Serve() = %v", err)
		}
	})
	return daemon.NewRemote(path)
}

// A client learns who this person is, and each network account's state, from the
// daemon.
func TestSelvesAndNetworksCrossTheSocket(t *testing.T) {
	t.Parallel()
	ids := []string{"@me:x", "whatsapp:359880000001@s.whatsapp.net"}
	state := daemon.NewState()
	online := time.Unix(1_700_000_000, 0)
	rows := []daemon.NetworkStatus{
		{Network: "Matrix", Account: "@me:x", Phase: daemon.PhaseLoggedOut, Detail: "no saved session; run `kith login`"},
		{Network: "WhatsApp", Account: "bg", Phase: daemon.PhaseOnline},
	}
	for _, row := range rows {
		state.Report(row, online)
	}
	remote := serveIdentity(t, selvesBackend{ids: ids}, state)

	if got, err := remote.Selves(context.Background()); err != nil || !slices.Equal(got, ids) {
		t.Errorf("Selves = (%v, %v), want %v", got, err, ids)
	}
	got, err := remote.Networks(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("Networks = (%v, %v), want two rows", got, err)
	}
	rows[1].At = online
	for i := range rows {
		if got[i].Network != rows[i].Network || got[i].Account != rows[i].Account || got[i].Phase != rows[i].Phase ||
			got[i].Detail != rows[i].Detail || !got[i].At.Equal(rows[i].At) {
			t.Errorf("row %d = %+v, want %+v", i, got[i], rows[i])
		}
	}
}
