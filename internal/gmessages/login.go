package gmessages

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Pairing is done as Messages for web does it, with the Google account the phone's
// Messages uses: kith takes the account's cookies from a browser signed in at
// messages.google.com, and the phone asks you to tap the emoji kith shows. (Google
// discontinued pairing by QR code; the bridge dropped it too.)

// ErrNoAccount is a pairing for a name no [[gmessages.account]] has.
var ErrNoAccount = errors.New("gmessages: no such [[gmessages.account]]")

const (
	cookiesHelp = "kith pairs with the Google account the phone's Messages uses. In a browser, sign in " +
		"at https://messages.google.com/web with that account, and open the developer tools (F12) " +
		"→ Network. Reload the page, pick any request to messages.google.com, and under Request " +
		"Headers copy the value of Cookie (it holds SID, HSID, SSID, APISID, SAPISID and OSID). A " +
		"\"Copy as cURL\" of that request works too. The request must be to messages.google.com " +
		"itself: OSID is sent nowhere else.\n\n" +
		"The cookies sign in to the Google account: whoever has them can act as you. kith keeps " +
		"them in the system keyring.\n\nOnly one Messages for web session is active at a time: pairing kith " +
		"takes it over from a browser or a bridge."
	emojiNote = "On the phone, Messages asks to pair: tap this emoji there."
)

// LoginNetwork is Google Messages and its accounts as a login lists them.
func (a *Adapter) LoginNetwork() api.LoginNetwork {
	n := api.LoginNetwork{Network: "gmessages", Label: "Google Messages", Detail: "an Android phone, paired with your Google account"}
	for _, account := range a.accountsNow() {
		n.Accounts = append(n.Accounts, api.LoginAccount{Name: account.Name})
	}
	return n
}

// Login pairs account with a phone, setting the account up first when it is new ("").
func (a *Adapter) Login(ctx context.Context, account string, talk api.LoginTalk) (api.LoginEnd, error) {
	if account == "" {
		name, err := a.setUp(ctx, talk)
		if err != nil {
			return api.LoginEnd{}, err
		}
		account = name
	}
	if !slices.ContainsFunc(a.accountsNow(), func(acc Account) bool { return acc.Name == account }) {
		return api.LoginEnd{}, fmt.Errorf("%w named %q", ErrNoAccount, account)
	}
	client, phone, err := a.pairByGoogle(ctx, talk)
	if err != nil {
		return api.LoginEnd{}, err
	}
	return a.finish(account, client, phone)
}

// pairByGoogle asks for the Google account's cookies, shows the emoji the phone asks
// for, and returns the paired client and the phone's ID. Cookies Google refuses are
// asked for again.
func (a *Adapter) pairByGoogle(ctx context.Context, talk api.LoginTalk) (*libgm.Client, string, error) {
	note := ""
	for {
		got, err := talk.Ask(ctx, note, api.LoginField{Key: "cookies", Label: "cookies", Secret: true, Help: cookiesHelp})
		if err != nil {
			return nil, "", err
		}
		cookies, err := parseCookies(got["cookies"])
		if err != nil {
			note = err.Error()
			continue
		}
		client, phone, err := a.gaia(ctx, talk, cookies)
		if err == nil {
			return client, phone, nil
		}
		if ctx.Err() != nil || !askAgain(err) {
			return nil, "", err
		}
		note = err.Error()
	}
}

// gaia pairs with the Google account's cookies.
func (a *Adapter) gaia(ctx context.Context, talk api.LoginTalk, cookies map[string]string) (*libgm.Client, string, error) {
	auth := libgm.NewAuthData()
	auth.SetCookies(cookies)
	client := a.newClient(auth)
	client.SetEventHandler(func(any) {})
	pairing, stop := context.WithCancel(context.WithoutCancel(ctx))
	defer stop()
	var emoji string
	var session *libgm.PairingSession
	err := a.guard("starting to pair", func() error {
		if err := client.FetchConfig(ctx); err != nil {
			return err //nolint:wrapcheck // read below
		}
		var err error
		emoji, session, err = client.StartGaiaPairing(ctx, pairing)
		return err //nolint:wrapcheck // read below
	})
	if err != nil {
		client.Disconnect()
		return nil, "", gaiaFailed(err)
	}
	if serr := talk.Show(ctx, emoji, emojiNote); serr != nil {
		client.Disconnect()
		return nil, "", serr
	}
	var phone string
	err = a.guard("pairing", func() (err error) {
		phone, err = client.FinishGaiaPairing(ctx, session)
		return err //nolint:wrapcheck // read below
	})
	if err != nil {
		client.Disconnect()
		return nil, "", gaiaFailed(err)
	}
	client.Disconnect()
	return client, phone, nil
}

// Google-account pairing failures, as the login says them.
var (
	errCookiesRefused = errors.New("google refused those cookies: copy them again from a browser signed in at messages.google.com")
	errNoPhone        = errors.New("no phone uses Messages with that Google account: open Messages on the phone and turn on " +
		"Google account pairing (profile picture → Device pairing)")
	errPhoneSilent = errors.New("the phone did not answer: is it on and online, with Messages open?")
	errWrongEmoji  = errors.New("the phone said that was the wrong emoji; log in again to try once more")
	errDeclined    = errors.New("the pairing was canceled on the phone")
	errTooSlow     = errors.New("no emoji was tapped on the phone in time; log in again")
)

// gaiaFailed says why a Google-account pairing failed.
func gaiaFailed(err error) error {
	switch {
	case errors.Is(err, events.ErrInvalidCredentials), errors.Is(err, libgm.ErrNoCookies):
		return fmt.Errorf("%w (%w)", errCookiesRefused, err)
	case errors.Is(err, libgm.ErrNoDevicesFound):
		return errNoPhone
	case errors.Is(err, libgm.ErrPairingInitTimeout):
		return errPhoneSilent
	case errors.Is(err, libgm.ErrIncorrectEmoji):
		return errWrongEmoji
	case errors.Is(err, libgm.ErrPairingCancelled):
		return errDeclined
	case errors.Is(err, libgm.ErrPairingTimeout):
		return errTooSlow
	}
	return fmt.Errorf("gmessages: pair with the Google account: %w", err)
}

// askAgain reports whether a Google-account pairing failure is worth new cookies.
func askAgain(err error) bool { return errors.Is(err, errCookiesRefused) }

// finish keeps a pairing as account's session and connects it.
func (a *Adapter) finish(account string, client *libgm.Client, phone string) (api.LoginEnd, error) {
	s := &Session{PhoneID: phone, Auth: client.AuthData}
	if err := s.valid(); err != nil {
		return api.LoginEnd{}, fmt.Errorf("gmessages: the phone's answer to pairing was incomplete: %w", err)
	}
	if other := a.pairedAs(phone, account); other != "" {
		return api.LoginEnd{}, fmt.Errorf("gmessages: that phone is the account %s already", other)
	}
	signIn := a.newSignIn(account)
	a.keeping.Lock()
	err := saveSession(a.secrets, account, s)
	a.keeping.Unlock()
	if err != nil {
		return api.LoginEnd{}, err
	}
	c := a.newConn(Account{Name: account}, s, &signIn)
	if !a.adopt(c) {
		return api.LoginEnd{}, fmt.Errorf("%w named %q: it left the config while pairing", ErrNoAccount, account)
	}
	a.log.Info("paired", "account", account, "phone", phone)
	go a.connect(a.runContext(), c, nil)
	return api.LoginEnd{Done: "paired with Google Messages on the phone"}, nil
}

// pairedAs is the other account already paired with phone, or "".
func (a *Adapter) pairedAs(phone, except string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for name, c := range a.conns {
		if name != except && c.phone == phone {
			return name
		}
	}
	return ""
}

// runContext is Start's context, which a pairing's connection outlives the login under.
func (a *Adapter) runContext() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.run == nil {
		return context.Background()
	}
	return a.run
}

// setUp asks for the new account's name and writes it into the config.
func (a *Adapter) setUp(ctx context.Context, talk api.LoginTalk) (string, error) {
	accounts := a.accountsNow()
	taken := make([]string, 0, len(accounts))
	for _, acc := range accounts {
		taken = append(taken, acc.Name)
	}
	name, err := api.AskName(ctx, talk, domain.FreeName("phone", taken), taken,
		"What kith calls this phone: `kith login gmessages <name>` pairs it again.")
	if err != nil {
		return "", err
	}
	if err := talk.Configure(ctx, api.LoginRecord{Table: "gmessages.account", Values: map[string]string{"name": name}}); err != nil {
		return "", err
	}
	talk.Named(name)
	return name, nil
}
