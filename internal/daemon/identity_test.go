package daemon_test

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// selvesBackend says who this person is.
type selvesBackend struct {
	apitest.Nop
	ids []string
	dir domain.Directory
}

func (b selvesBackend) Selves(context.Context) ([]string, error) { return b.ids, nil }

func (b selvesBackend) Directory(context.Context) (domain.Directory, error) { return b.dir, nil }

// numbersNamed is a directory naming each number as one address book saved it.
func numbersNamed(names map[string]string) domain.Directory {
	var rows []domain.PersonName
	for digits, name := range names {
		rows = append(rows, domain.PersonName{Source: "phone", ID: domain.PhoneID(digits), Name: name, Rank: domain.RankSaved})
	}
	return domain.NewDirectory(rows, nil)
}

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
	ids := []string{"@me:x", "whatsapp:44880000001@s.whatsapp.net"}
	state := daemon.NewState()
	online := time.Unix(1_700_000_000, 0)
	rows := []daemon.NetworkStatus{
		{Network: "Matrix", Account: "@me:x", Phase: daemon.PhaseLoggedOut, Detail: "no saved session; run `kith login`"},
		{Network: "WhatsApp", Account: "home", Phase: daemon.PhaseOnline},
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

// The directory crosses the socket whole: every name, rank and link, so a client
// names people as the daemon would.
func TestTheDirectoryCrossesTheSocket(t *testing.T) {
	t.Parallel()
	dir := domain.NewDirectory([]domain.PersonName{
		{Source: "whatsapp:1", ID: domain.PhoneID("15550100001"), Name: "Dana", Rank: domain.RankSaved},
		{Source: "telegram:2", ID: "telegram:7", Name: "Eli", Rank: domain.RankChosen},
	}, []domain.PersonLink{{Source: "telegram:2", ID: "telegram:7", Other: domain.PhoneID("15550100002")}})
	remote := serveIdentity(t, selvesBackend{dir: dir}, daemon.NewState())
	got, err := remote.Directory(context.Background())
	if err != nil || !got.Equal(dir) {
		t.Fatalf("Directory = (%v, %v), want %v", got.Names(), err, dir.Names())
	}
	if eli, _ := got.Named("+15550100002"); eli != "Eli" {
		t.Errorf("Eli's number is named %q across the socket, want Eli by the link", eli)
	}
}
