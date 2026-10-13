package meta

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/bloks"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
	"go.mau.fi/util/exhttp"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Logging in signs in as the Messenger Lite app: messagix leads the steps (email or
// phone and password, then whichever second factor the account has) and kith asks each
// one through the login conversation. It ends with web cookies, which are checked by
// loading the messages page and kept as the session.

// ErrNoAccount is a sign-in for a name no [[messenger.account]] has.
var ErrNoAccount = errors.New("meta: no such [[messenger.account]]")

// approveElsewhere is shown while Meta waits for the login to be approved on a
// device already signed in, which shows no code of its own.
const approveElsewhere = "approve it in the Facebook or Messenger app"

// LoginNetwork is Messenger and its accounts as a login lists them.
func (a *Adapter) LoginNetwork() api.LoginNetwork {
	n := api.LoginNetwork{Network: "messenger", Label: "Messenger", Detail: "a Meta account, signed in with its password"}
	for _, account := range a.accountsNow() {
		n.Accounts = append(n.Accounts, api.LoginAccount{Name: account.Name})
	}
	return n
}

// Login signs account in with its Meta account's password, setting the account up
// first when it is new (""). Meta's refusal ends the login, saying why.
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
	client := messagix.NewClient(&cookies.Cookies{Platform: loginPlatform}, a.zerolog(),
		&messagix.Config{ClientSettings: exhttp.SensibleClientSettings})
	var input map[string]string
	for {
		var step *bridgev2.LoginStep
		var got *cookies.Cookies
		err := a.guard("signing in", func() (err error) {
			step, got, err = client.MessengerLite.DoLoginSteps(ctx, input)
			return err //nolint:wrapcheck // the caller wraps it and reads its kind (errors.Is)
		})
		if err != nil {
			return api.LoginEnd{}, loginFailed(err)
		}
		if got != nil {
			return a.finishLogin(ctx, account, client, got)
		}
		if input, err = answer(ctx, talk, step); err != nil {
			return api.LoginEnd{}, err
		}
	}
}

// answer asks the person what step wants: fields to fill, or a wait for approval on
// another device (nil answers, so the next call waits for it).
func answer(ctx context.Context, talk api.LoginTalk, step *bridgev2.LoginStep) (map[string]string, error) {
	switch {
	case step.Type == bridgev2.LoginStepTypeUserInput && step.UserInputParams != nil:
		return askFields(ctx, talk, step.Instructions, step.UserInputParams.Fields)
	case step.Type == bridgev2.LoginStepTypeDisplayAndWait:
		code := approveElsewhere
		if p := step.DisplayAndWaitParams; p != nil && p.Type == bridgev2.LoginDisplayTypeCode && p.Data != "" {
			code = p.Data
		}
		return nil, talk.Show(ctx, code, step.Instructions)
	}
	return nil, fmt.Errorf("meta: Meta asked for a step kith cannot take (%s); sign in once at facebook.com, "+
		"then log in here again", step.Type)
}

// askFields asks for fields until each answer passes the field's own check.
func askFields(ctx context.Context, talk api.LoginTalk, note string, fields []bridgev2.LoginInputDataField) (map[string]string, error) {
	asked := make([]api.LoginField, 0, len(fields))
	for i := range fields {
		asked = append(asked, loginField(&fields[i]))
	}
	for {
		got, err := talk.Ask(ctx, note, asked...)
		if err != nil {
			return nil, err
		}
		refused := ""
		for i := range fields {
			f := &fields[i]
			value := strings.TrimSpace(got[f.ID])
			if f.Type == bridgev2.LoginInputFieldTypePassword {
				value = got[f.ID] // a password's spaces are part of it
			}
			if f.Validate != nil {
				cleaned, err := f.Validate(value)
				if err != nil {
					refused = fmt.Sprintf("%s: %v", strings.ToLower(f.Name), err)
					break
				}
				value = cleaned
			}
			got[f.ID] = value
		}
		if refused == "" {
			return got, nil
		}
		note = refused
	}
}

// loginField is one of messagix's fields as kith asks it.
func loginField(f *bridgev2.LoginInputDataField) api.LoginField {
	help := f.Description
	if len(f.Options) > 0 {
		help = strings.TrimSpace(help + "\n\nOne of: " + strings.Join(f.Options, ", ") + ".")
	}
	return api.LoginField{
		Key: f.ID, Label: strings.ToLower(f.Name), Help: help, Value: f.DefaultValue,
		Secret: f.Type == bridgev2.LoginInputFieldTypePassword,
	}
}

// loginFailed says why Meta refused a login.
func loginFailed(err error) error {
	if errors.As(err, &bloks.CheckpointError{}) {
		return errors.New("meta: Meta wants a security check for this login: sign in once at facebook.com " +
			"and finish it there, then log in here again")
	}
	return fmt.Errorf("meta: sign in: %w", err)
}

// finishLogin checks the cookies a login ended with by loading the messages page, keeps
// them as account's session, and connects it.
func (a *Adapter) finishLogin(ctx context.Context, account string, login *messagix.Client, got *cookies.Cookies) (api.LoginEnd, error) {
	s := Session{MetaID: got.GetUserID(), Platform: loginPlatform, Native: login.MessengerLite.GetNativeSession()}.withCookies(got)
	if err := s.valid(); err != nil {
		return api.LoginEnd{}, fmt.Errorf("meta: Meta's answer to the login was incomplete: %w", err)
	}
	if other := a.signedInAs(s.MetaID, account); other != "" {
		return api.LoginEnd{}, fmt.Errorf("meta: that Meta account is the account %s already", other)
	}
	client := a.clientFor(s)
	var user types.UserInfo
	var inbox *table.LSTable
	err := a.guard("loading the messages page", func() (err error) {
		user, inbox, err = client.LoadMessagesPage(ctx)
		return err //nolint:wrapcheck // the caller wraps it and reads its kind (errors.Is)
	})
	if err != nil {
		if over := sessionOver(err, account); over != "" {
			return api.LoginEnd{}, errors.New("meta: " + over)
		}
		return api.LoginEnd{}, fmt.Errorf("meta: load the messages page: %w", err)
	}
	if user.GetFBID() != s.MetaID {
		return api.LoginEnd{}, fmt.Errorf("meta: Meta signed in %d, not the account the login was for (%d)", user.GetFBID(), s.MetaID)
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
		client.Disconnect()
		return api.LoginEnd{}, fmt.Errorf("%w named %q: it left the config while signing in", ErrNoAccount, account)
	}
	go a.connect(a.runContext(), c, &loaded{client: client, inbox: inbox})
	return api.LoginEnd{Done: "signed in to Messenger as " + user.GetName()}, nil
}

// signedInAs is the other account already signed in as metaID, or "".
func (a *Adapter) signedInAs(metaID int64, except string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for name, c := range a.conns {
		if name != except && c.metaID == metaID {
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
	name, err := api.AskName(ctx, talk, domain.FreeName("meta", taken), taken,
		"What kith calls this Meta account: `kith login messenger <name>` signs it in again.")
	if err != nil {
		return "", err
	}
	if err := talk.Configure(ctx, api.LoginRecord{Table: "messenger.account", Values: map[string]string{"name": name}}); err != nil {
		return "", err
	}
	talk.Named(name)
	return name, nil
}
