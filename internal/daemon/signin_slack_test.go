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

// slackSignIn signs in as told, keeping what it was handed.
type slackSignIn struct {
	account, token, cookie string
	answer                 api.SlackSignedIn
	fail                   error
}

func (s *slackSignIn) SignInSlack(_ context.Context, account, token, cookie string) (api.SlackSignedIn, error) {
	s.account, s.token, s.cookie = account, token, cookie
	return s.answer, s.fail
}

// serveSlack is a daemon whose Slack sign-in is signIn (nil: [slack] off).
func serveSlack(t *testing.T, signIn api.SlackSignIn) *daemon.Remote {
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
			Backend: apitest.Nop{}, Streams: daemon.NewStreams(), State: daemon.NewState(), Slack: signIn,
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

// `kith login slack` hands the daemon the account, token and cookie, and hears where
// it signed in; a refusal and Slack being off reach it as such.
func TestSlackSignInCrossesTheSocket(t *testing.T) {
	t.Parallel()
	s := &slackSignIn{answer: api.SlackSignedIn{Workspace: "Acme", User: "dana"}}
	got, err := serveSlack(t, s).SignInSlack(context.Background(), "work", "xoxc-1", "xoxd-2")
	if err != nil || got != s.answer || s.account != "work" || s.token != "xoxc-1" || s.cookie != "xoxd-2" {
		t.Errorf("SignInSlack = (%+v, %v), handed %q %q %q", got, err, s.account, s.token, s.cookie)
	}
	if _, err := serveSlack(t, &slackSignIn{fail: errors.New("invalid_auth")}).
		SignInSlack(context.Background(), "work", "x", "y"); err == nil {
		t.Error("a refused session came back as success")
	}
	if _, err := serveSlack(t, nil).SignInSlack(context.Background(), "work", "x", "y"); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("[slack] off = %v, want ErrNetworkOff", err)
	}
}
