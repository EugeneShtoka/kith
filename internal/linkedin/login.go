package linkedin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.mau.fi/mautrix-linkedin/pkg/linkedingo"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Logging in takes the session a browser holds: LinkedIn has no sign-in an app can
// lead (the bridge opens a web view for it). kith asks for the cookies of a request to
// www.linkedin.com and checks them by reading the account's own profile.

// ErrNoAccount is a sign-in for a name no [[linkedin.account]] has.
var ErrNoAccount = errors.New("linkedin: no such [[linkedin.account]]")

// cookiesHelp is how to copy the cookies, shown with the field.
const cookiesHelp = "kith signs in with the session your browser holds. In a private window, sign in " +
	"at https://www.linkedin.com and open the developer tools (F12) → Network. Reload the page, " +
	"pick any request to www.linkedin.com, and under Request Headers copy the value of Cookie " +
	"(it holds li_at and JSESSIONID). A \"Copy as cURL\" of that request works too, on one line. " +
	"A terminal takes about 4,000 characters on a line: if the header is longer, paste just " +
	"li_at=…; JSESSIONID=… with their values from Application → Cookies.\n\n" +
	"The cookies are a session: whoever has them can read and write as you. kith keeps them in the " +
	"system keyring. Then close the private window without signing out: signing out there ends " +
	"kith's session too."

// LoginNetwork is LinkedIn and its accounts as a login lists them.
func (a *Adapter) LoginNetwork() api.LoginNetwork {
	n := api.LoginNetwork{Network: "linkedin", Label: "LinkedIn", Detail: "an account, signed in with your browser's session"}
	for _, account := range a.accountsNow() {
		n.Accounts = append(n.Accounts, api.LoginAccount{Name: account.Name})
	}
	return n
}

// Login signs account in with a browser's cookies, setting the account up first when
// it is new (""). Cookies LinkedIn refuses are asked for again, saying why.
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
	note := ""
	for {
		got, err := talk.Ask(ctx, note, api.LoginField{Key: "cookies", Label: "cookies", Secret: true, Help: cookiesHelp})
		if err != nil {
			return api.LoginEnd{}, err
		}
		paste, err := parsePaste(got["cookies"])
		if err != nil {
			note = err.Error()
			continue
		}
		end, err := a.signIn(ctx, account, paste)
		if err != nil {
			if ctx.Err() != nil {
				return api.LoginEnd{}, err
			}
			note = err.Error()
			continue
		}
		return end, nil
	}
}

// signIn checks paste's cookies by reading the profile they belong to, keeps them as
// account's session, and connects it.
func (a *Adapter) signIn(ctx context.Context, account string, paste pasted) (api.LoginEnd, error) {
	s := Session{Cookies: paste.cookies, Track: paste.track, PageInstance: paste.pageInstance}
	profile, err := a.profileOf(ctx, a.clientFor(ctx, s, linkedingo.Handlers{}))
	if err != nil {
		a.log.Info("LinkedIn refused the sign-in", "account", account, "err", err)
		if why := refusal(err); why != "" {
			return api.LoginEnd{}, errors.New(why)
		}
		return api.LoginEnd{}, fmt.Errorf("LinkedIn did not answer: %w", err)
	}
	me := profile.MiniProfile
	s.ProfileID = me.EntityURN.ID()
	if s.valid() != nil {
		return api.LoginEnd{}, errors.New("LinkedIn's answer named no profile; copy the cookies again")
	}
	if other := a.signedInAs(s.ProfileID, account); other != "" {
		return api.LoginEnd{}, fmt.Errorf("that LinkedIn account is the account %s already", other)
	}
	signIn := a.newSignIn(account)
	a.keeping.Lock()
	err = saveSession(a.secrets, account, s)
	a.keeping.Unlock()
	if err != nil {
		return api.LoginEnd{}, err
	}
	c := a.newConn(Account{Name: account}, s, &signIn)
	if !a.adopt(c) {
		return api.LoginEnd{}, fmt.Errorf("%w named %q: it left the config while signing in", ErrNoAccount, account)
	}
	a.log.Info("signed in", "account", account, "profile", s.ProfileID)
	go a.connect(a.runContext(), c, true)
	return api.LoginEnd{Done: "signed in to LinkedIn as " + nameOf(me)}, nil
}

// nameOf is how a profile reads.
func nameOf(p linkedingo.MiniProfile) string {
	if name := strings.TrimSpace(p.FirstName + " " + p.LastName); name != "" {
		return name
	}
	return p.PublicIdentifier
}

// signedInAs is the other account already signed in as profile, or "".
func (a *Adapter) signedInAs(profile, except string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for name, c := range a.conns {
		if name != except && c.profile == profile {
			return name
		}
	}
	return ""
}

// runContext is Start's context, which a login's connection outlives the login under.
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
	name, err := api.AskName(ctx, talk, domain.FreeName("linkedin", taken), taken,
		"What kith calls this LinkedIn account: `kith login linkedin <name>` signs it in again.")
	if err != nil {
		return "", err
	}
	if err := talk.Configure(ctx, api.LoginRecord{Table: "linkedin.account", Values: map[string]string{"name": name}}); err != nil {
		return "", err
	}
	talk.Named(name)
	return name, nil
}
