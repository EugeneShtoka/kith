package daemon_test

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/daemon"
)

// leader is a network whose login sets an account up, shows a code, asks for it, and
// is done; or fails as told.
type leader struct {
	fail error
}

func (l leader) LoginNetwork() api.LoginNetwork {
	return api.LoginNetwork{Network: "fake", Label: "Fake", Accounts: []api.LoginAccount{{Name: "home", Detail: "+1"}}}
}

func (l leader) Login(ctx context.Context, account string, talk api.LoginTalk) (api.LoginEnd, error) {
	if account == "" {
		got, err := talk.Ask(ctx, "", api.LoginField{Key: "name", Label: "call it", Value: "work"},
			api.LoginField{Key: "token", Label: "token", Secret: true})
		if err != nil {
			return api.LoginEnd{}, err
		}
		if err := talk.Configure(ctx, api.LoginRecord{Table: "fake.account", Values: map[string]string{"name": got["name"]}}); err != nil {
			return api.LoginEnd{}, err
		}
		talk.Named(got["name"])
	}
	if err := talk.Show(ctx, "ABCD-1234", "type it on the phone"); err != nil {
		return api.LoginEnd{}, err
	}
	note := ""
	for {
		got, err := talk.Ask(ctx, note, api.LoginField{Key: "code", Label: "code"})
		if err != nil {
			return api.LoginEnd{}, err
		}
		if l.fail != nil {
			return api.LoginEnd{}, l.fail
		}
		if got["code"] == "1234" {
			return api.LoginEnd{Done: "logged in"}, nil
		}
		note = "wrong code"
	}
}

// serveLogins is a daemon over the socket whose logins are leaders'.
func serveLogins(t *testing.T, leaders ...api.LoginLeader) *daemon.Remote {
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
			Backend: apitest.Nop{}, Streams: daemon.NewStreams(), State: daemon.NewState(),
			Logins: daemon.NewLogins(ctx, func() []api.LoginLeader { return leaders }),
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

// A login crosses the socket step by step: fields to ask (a secret marked so), a
// record to write, a code to show, a wrong answer asked again with a note, the end;
// the account is named once set up.
func TestALoginCrossesTheSocketStepByStep(t *testing.T) {
	t.Parallel()
	remote := serveLogins(t, leader{})
	ctx := t.Context()
	networks, err := remote.LoginNetworks(ctx)
	if err != nil || len(networks) != 1 || networks[0].Network != "fake" || networks[0].Accounts[0].Name != "home" {
		t.Fatalf("networks = (%+v, %v)", networks, err)
	}
	step, err := remote.BeginLogin(ctx, "fake", "")
	if err != nil || len(step.Ask) != 2 || step.Ask[0].Value != "work" || !step.Ask[1].Secret || step.Login == "" {
		t.Fatalf("first step = (%+v, %v), want the name and a secret token", step, err)
	}
	step, err = remote.AnswerLogin(ctx, step.Login, map[string]string{"name": "work", "token": "t"})
	if err != nil || step.Configure == nil || step.Configure.Table != "fake.account" ||
		!maps.Equal(step.Configure.Values, map[string]string{"name": "work"}) {
		t.Fatalf("second step = (%+v, %v), want the record", step, err)
	}
	step, err = remote.AnswerLogin(ctx, step.Login, nil)
	if err != nil || step.Code != "ABCD-1234" || step.Note != "type it on the phone" || step.Account != "work" {
		t.Fatalf("third step = (%+v, %v), want the code, for work", step, err)
	}
	step, err = remote.AnswerLogin(ctx, step.Login, nil)
	if err != nil || len(step.Ask) != 1 || step.Ask[0].Key != "code" {
		t.Fatalf("fourth step = (%+v, %v), want the code asked", step, err)
	}
	step, err = remote.AnswerLogin(ctx, step.Login, map[string]string{"code": "0000"})
	if err != nil || step.Note != "wrong code" {
		t.Fatalf("a wrong code = (%+v, %v), want it asked again", step, err)
	}
	login := step.Login
	step, err = remote.AnswerLogin(ctx, login, map[string]string{"code": "1234"})
	if err != nil || step.Done != "logged in" {
		t.Fatalf("last step = (%+v, %v), want done", step, err)
	}
	if _, err := remote.AnswerLogin(ctx, login, nil); err == nil {
		t.Error("an answer after the end went through")
	}
}

// A login that fails ends with its error; one of no network is refused as off.
func TestALoginFailureEndsIt(t *testing.T) {
	t.Parallel()
	remote := serveLogins(t, leader{fail: errors.New("refused by the network")})
	ctx := t.Context()
	step, err := remote.BeginLogin(ctx, "fake", "home")
	if err == nil {
		step, err = remote.AnswerLogin(ctx, step.Login, nil)
	}
	if err == nil {
		_, err = remote.AnswerLogin(ctx, step.Login, map[string]string{"code": "1234"})
	}
	if err == nil || err.Error() == "" {
		t.Errorf("a failing login = %v, want its error", err)
	}
	if _, err := remote.BeginLogin(ctx, "nothing", ""); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("a login of no network = %v, want network off", err)
	}
}
