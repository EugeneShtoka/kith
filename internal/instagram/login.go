package instagram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.mau.fi/util/exhttp"

	"go.mau.fi/mautrix-meta/pkg/instameow"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// ErrNoAccount is a login for a name no [[instagram.account]] has.
var ErrNoAccount = errors.New("instagram: no such [[instagram.account]]")

const cookiesHelp = "kith signs in with your browser's Instagram session. In a browser signed in to " +
	"Instagram, open https://www.instagram.com/direct/inbox/ and the developer tools (F12) → Network. " +
	"Reload, pick the first request to www.instagram.com, and copy the value of its Cookie request " +
	"header (it holds sessionid, csrftoken and ds_user_id); a whole \"Copy as cURL\" of that request " +
	"works too.\n\nThe cookies sign in to your Instagram account: whoever has them can act as you. kith " +
	"keeps them in the system keyring. (Signing in with a password reaches Instagram but stops at a " +
	"cookie-consent page kith cannot answer; your browser has answered it already.)"

// LoginNetwork is Instagram and its accounts as a login lists them.
func (a *Adapter) LoginNetwork() api.LoginNetwork {
	n := api.LoginNetwork{Network: "instagram", Label: "Instagram", Detail: "direct messages, with your browser's Instagram session"}
	for _, account := range a.accountsNow() {
		n.Accounts = append(n.Accounts, api.LoginAccount{Name: account.Name})
	}
	return n
}

// Login signs account in with an instagram.com session's cookies, setting the account
// up first when it is new (""). Cookies Instagram refuses are asked for again.
func (a *Adapter) Login(ctx context.Context, account string, talk api.LoginTalk) (api.LoginEnd, error) {
	if account == "" {
		name, err := a.setUp(ctx, talk)
		if err != nil {
			return api.LoginEnd{}, err
		}
		account = name
	}
	if !a.configured(account) {
		return api.LoginEnd{}, fmt.Errorf("%w named %q", ErrNoAccount, account)
	}
	note := ""
	for {
		got, err := talk.Ask(ctx, note, api.LoginField{Key: "cookies", Label: "cookies", Secret: true, Help: cookiesHelp})
		if err != nil {
			return api.LoginEnd{}, err
		}
		s, err := a.check(ctx, account, got["cookies"])
		if err == nil {
			return a.finish(account, s)
		}
		if ctx.Err() != nil {
			return api.LoginEnd{}, err
		}
		note = err.Error()
	}
}

// check reads the cookies and loads the inbox with them, which says whose they are.
func (a *Adapter) check(ctx context.Context, account, paste string) (*Session, error) {
	c, err := parseCookies(paste)
	if err != nil {
		return nil, err
	}
	client := instameow.NewClient(instameow.ClientParams{
		Cookies: c, Log: a.zerolog(account), Settings: exhttp.SensibleClientSettings,
	})
	if _, _, err := client.LoadIndex(ctx); err != nil {
		if why, over := sessionOver(err); over {
			return nil, errors.New(why)
		}
		return nil, fmt.Errorf("instagram refused those cookies: %w", err)
	}
	return &Session{Cookies: client.GetCookies(), Self: selfOf(client)}, nil
}

// finish keeps the session and connects the account.
func (a *Adapter) finish(account string, s *Session) (api.LoginEnd, error) {
	if other := a.signedInAs(s.Self, account); other != "" {
		return api.LoginEnd{}, fmt.Errorf("instagram: that Instagram account is the account %s already", other)
	}
	a.mu.Lock()
	old := a.conns[account]
	delete(a.conns, account)
	run := a.run
	a.mu.Unlock()
	if old != nil {
		old.close()
	}
	a.keeping.Lock()
	err := saveSession(a.secrets, account, s)
	a.keeping.Unlock()
	if err != nil {
		return api.LoginEnd{}, err
	}
	a.log.Info("signed in", "account", account, "self", s.Self)
	if run != nil {
		a.connect(run, Account{Name: account}, s)
	}
	return api.LoginEnd{Done: "signed in to Instagram"}, nil
}

// signedInAs is the other account already signed in as self, or "".
func (a *Adapter) signedInAs(self, except string) string {
	for _, account := range a.accountsNow() {
		if account.Name == except {
			continue
		}
		if s, ok, err := loadSession(a.secrets, account.Name); err == nil && ok && s.Self == self {
			return account.Name
		}
	}
	return ""
}

// setUp asks what to call a new account and has it written into the config.
func (a *Adapter) setUp(ctx context.Context, talk api.LoginTalk) (string, error) {
	accounts := a.accountsNow()
	taken := make([]string, 0, len(accounts))
	for _, acc := range accounts {
		taken = append(taken, acc.Name)
	}
	name, err := api.AskName(ctx, talk, domain.FreeName("me", taken), taken,
		"What kith calls this Instagram account: `kith login instagram <name>` signs it in again.")
	if err != nil {
		return "", err
	}
	if err := talk.Configure(ctx, api.LoginRecord{Table: "instagram.account", Values: map[string]string{"name": name}}); err != nil {
		return "", err
	}
	talk.Named(name)
	return name, nil
}

// Accounts is each signed-in account, by the user ID its rooms are under.
func (a *Adapter) Accounts() []domain.Account {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]domain.Account, 0, len(a.conns))
	for _, c := range a.conns {
		out = append(out, domain.Account{Network: domain.ProtocolInstagram, On: owner(c.self), Name: c.account.Name})
	}
	slices.SortFunc(out, func(x, y domain.Account) int { return strings.Compare(x.Name, y.Name) })
	return out
}
