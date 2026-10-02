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

// matrixLogin logs in as told.
type matrixLogin struct {
	password string
	answer   api.MatrixLoggedIn
	fail     error
}

func (l *matrixLogin) LoginMatrix(_ context.Context, password string) (api.MatrixLoggedIn, error) {
	l.password = password
	return l.answer, l.fail
}

// serveLogin is a daemon whose Matrix login is login (nil: Matrix not configured).
func serveLogin(t *testing.T, login api.MatrixLogin) *daemon.Remote {
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
			Backend: apitest.Nop{}, Streams: daemon.NewStreams(), State: daemon.NewState(), Matrix: login,
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

// `kith login` hands the daemon the password and hears who logged in and whether
// Matrix started on it.
func TestMatrixLoginCrossesTheSocket(t *testing.T) {
	t.Parallel()
	l := &matrixLogin{answer: api.MatrixLoggedIn{UserID: "@me:x", DeviceID: "DEV", Started: true}}
	got, err := serveLogin(t, l).LoginMatrix(context.Background(), "hunter2")
	if err != nil || got != l.answer || l.password != "hunter2" {
		t.Errorf("LoginMatrix = (%+v, %v), password %q; want %+v with the password passed", got, err, l.password, l.answer)
	}
}

// A refused password, and Matrix not being configured, reach the command as such.
func TestMatrixLoginFailuresCrossTheSocket(t *testing.T) {
	t.Parallel()
	if _, err := serveLogin(t, &matrixLogin{fail: errors.New("M_FORBIDDEN: invalid password")}).
		LoginMatrix(context.Background(), "x"); err == nil {
		t.Error("a refused login came back as success")
	}
	if _, err := serveLogin(t, nil).LoginMatrix(context.Background(), "x"); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("Matrix not configured = %v, want ErrNetworkOff", err)
	}
}
