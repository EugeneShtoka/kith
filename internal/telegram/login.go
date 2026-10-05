package telegram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Logging in is a conversation: the number is given, Telegram sends a code, the code
// is given, and the password when the account has two-step verification on. The
// daemon hears its parts in separate calls (the code exists only once it was sent),
// so each login runs in a goroutine of its own, on a connection of its own, and the
// calls hand it their answers one at a time.

// loginTimeout bounds a login from its code being sent to its last answer; Telegram's
// codes expire sooner.
const loginTimeout = 15 * time.Minute

// ErrNoAccount is a login for a name no [[telegram.account]] has.
var ErrNoAccount = errors.New("telegram: no such [[telegram.account]]")

var (
	// errNoLogin is an answer with no login under way to take it.
	errNoLogin = errors.New("telegram: no login is under way for this account; have the code sent first")
	// errSuperseded is a login overtaken by a newer one of the same account, or by its
	// leaving the config.
	errSuperseded = errors.New("telegram: another login of this account began meanwhile")
	// errNeedCode is an answer with no code, before the code was taken.
	errNeedCode = errors.New("telegram: the code Telegram sent is needed")
)

// login is an account's login under way.
type login struct {
	// gen is which of the account's logins this is (see Adapter.newSignIn).
	gen     int
	answers chan loginAnswer
	// ended is closed when the conversation ends: no answer is taken after.
	ended  <-chan struct{}
	cancel context.CancelFunc
	// turn lets one answer at a time be heard.
	turn chan struct{}
}

// loginAnswer is one call's answer, and where its outcome goes. reply holds one, so
// the conversation never waits on a caller that left.
type loginAnswer struct {
	code, password string
	reply          chan loginReply
}

type loginReply struct {
	in  api.TelegramSignedIn
	err error
}

// codeSent is how the conversation's first step went.
type codeSent struct {
	via string
	err error
}

// account is the configured account called name.
func (a *Adapter) account(name string) (Account, error) {
	accounts := a.accountsNow()
	i := slices.IndexFunc(accounts, func(acc Account) bool { return acc.Name == name })
	if i < 0 {
		return Account{}, fmt.Errorf("%w named %q", ErrNoAccount, name)
	}
	return accounts[i], nil
}

// SendTelegramCode starts the named account's login over: Telegram sends it a code,
// through app (the zero App: kith's own).
func (a *Adapter) SendTelegramCode(ctx context.Context, name string, given api.TelegramApp) (api.TelegramCodeSent, error) {
	account, err := a.account(name)
	if err != nil {
		return api.TelegramCodeSent{}, err
	}
	app, err := appFor(given)
	if err != nil {
		return api.TelegramCodeSent{}, err
	}
	return a.beginLogin(ctx, account, app, a.newClient)
}

// beginLogin runs a login of account through dial until the code is sent; the rest
// of it waits for SignInTelegram.
func (a *Adapter) beginLogin(ctx context.Context, account Account, app App, dial dialer) (api.TelegramCodeSent, error) {
	storage := &session.StorageMemory{}
	client := dial(app, storage)
	run, cancel := context.WithTimeout(a.runContext(), loginTimeout)
	l := &login{answers: make(chan loginAnswer), ended: run.Done(), cancel: cancel, turn: make(chan struct{}, 1)}
	a.mu.Lock()
	if old := a.logins[account.Name]; old != nil {
		old.cancel()
	}
	a.signIns[account.Name]++
	l.gen = a.signIns[account.Name]
	a.logins[account.Name] = l
	a.mu.Unlock()

	sent := make(chan codeSent, 1)
	go func() {
		defer a.endLogin(account.Name, l)
		err := client.Run(run, func(ctx context.Context) error {
			return a.converse(ctx, account, app, client, storage, l, sent, dial)
		})
		if err != nil && run.Err() == nil {
			a.log.Info("a login ended", "account", account.Name, "err", err)
		}
		// The code's fate untold: the connection itself failed.
		select {
		case sent <- codeSent{err: fmt.Errorf("telegram: reach Telegram: %w", cmpErr(err, run.Err()))}:
		default:
		}
	}()
	select {
	case s := <-sent:
		if s.err != nil {
			return api.TelegramCodeSent{}, s.err
		}
		return api.TelegramCodeSent{Via: s.via}, nil
	case <-ctx.Done():
		a.endLogin(account.Name, l)
		return api.TelegramCodeSent{}, ctx.Err() //nolint:wrapcheck // the caller's own
	}
}

// cmpErr is err, or alt when err is nil.
func cmpErr(err, alt error) error {
	if err != nil {
		return err
	}
	if alt != nil {
		return alt
	}
	return errors.New("the connection closed")
}

// endLogin cancels l and forgets it, unless a newer login replaced it.
func (a *Adapter) endLogin(name string, l *login) {
	l.cancel()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.logins[name] == l {
		delete(a.logins, name)
	}
}

// converse is the login over a connected client: the code sent, then answers taken
// until one signs in, one fails for good, or the login ends.
func (a *Adapter) converse(
	ctx context.Context, account Account, app App, client *telegram.Client, storage *session.StorageMemory,
	l *login, sent chan<- codeSent, dial dialer,
) error {
	phone := "+" + account.Digits
	code, err := client.Auth().SendCode(ctx, phone, auth.SendCodeOptions{})
	if err != nil {
		sent <- codeSent{err: describeSendCode(err)}
		return nil
	}
	pending, ok := code.(*tg.AuthSentCode)
	if !ok {
		sent <- codeSent{err: errors.New("telegram: logged in without a code, which kith does not ask for")}
		return nil
	}
	sent <- codeSent{via: codeVia(pending.Type)}
	codeTaken := false
	for {
		var answer loginAnswer
		select {
		case <-ctx.Done():
			return ctx.Err() //nolint:wrapcheck // the login's own end
		case answer = <-l.answers:
		}
		user, err := a.signIn(ctx, client, phone, pending.PhoneCodeHash, answer, &codeTaken)
		if err == nil {
			creds := Credentials{App: app, User: user.ID}
			creds.Session, err = storage.Bytes(nil)
			if err == nil {
				err = a.keepLogin(account, l.gen, creds)
			}
			if err == nil {
				answer.reply <- loginReply{in: api.TelegramSignedIn{Name: userName(user), ID: personID(user.ID)}}
				a.connectAs(account, creds, l.gen, dial)
				return nil
			}
		}
		answer.reply <- loginReply{err: err}
		if !errors.Is(err, api.ErrBadCode) && !errors.Is(err, api.ErrPasswordNeeded) && !errors.Is(err, api.ErrBadPassword) &&
			!errors.Is(err, errNeedCode) {
			return nil
		}
	}
}

// signIn tries one answer: the code, then the password once Telegram asked for one
// (codeTaken). The user is whom it signed in as.
func (a *Adapter) signIn(
	ctx context.Context, client *telegram.Client, phone, hash string, answer loginAnswer, codeTaken *bool,
) (*tg.User, error) {
	if !*codeTaken {
		if answer.code == "" {
			return nil, errNeedCode
		}
		in, err := client.Auth().SignIn(ctx, phone, answer.code, hash)
		switch {
		case errors.Is(err, auth.ErrPasswordAuthNeeded):
			*codeTaken = true
		case err != nil:
			return nil, describeSignIn(err)
		default:
			return signedInAs(in)
		}
	}
	if answer.password == "" {
		return nil, api.ErrPasswordNeeded
	}
	in, err := client.Auth().Password(ctx, answer.password)
	if errors.Is(err, auth.ErrPasswordInvalid) {
		return nil, fmt.Errorf("telegram: %w", api.ErrBadPassword)
	}
	if err != nil {
		return nil, fmt.Errorf("telegram: check the password: %w", err)
	}
	return signedInAs(in)
}

// signedInAs is the user an authorization is for.
func signedInAs(in *tg.AuthAuthorization) (*tg.User, error) {
	user, ok := in.User.AsNotEmpty()
	if !ok {
		return nil, errors.New("telegram: signed in, but Telegram did not say as whom")
	}
	return user, nil
}

// SignInTelegram hands the named account's login its answer: the code, and the
// password when one was asked for.
func (a *Adapter) SignInTelegram(ctx context.Context, name, code, password string) (api.TelegramSignedIn, error) {
	a.mu.Lock()
	l := a.logins[name]
	a.mu.Unlock()
	if l == nil {
		return api.TelegramSignedIn{}, errNoLogin
	}
	select {
	case l.turn <- struct{}{}:
		defer func() { <-l.turn }()
	case <-l.ended:
		return api.TelegramSignedIn{}, errNoLogin
	case <-ctx.Done():
		return api.TelegramSignedIn{}, ctx.Err() //nolint:wrapcheck // the caller's own
	}
	answer := loginAnswer{code: code, password: password, reply: make(chan loginReply, 1)}
	select {
	case l.answers <- answer:
	case <-l.ended:
		return api.TelegramSignedIn{}, errNoLogin
	case <-ctx.Done():
		return api.TelegramSignedIn{}, ctx.Err() //nolint:wrapcheck // the caller's own
	}
	select {
	case r := <-answer.reply:
		return r.in, r.err
	case <-ctx.Done():
		return api.TelegramSignedIn{}, ctx.Err() //nolint:wrapcheck // the caller's own
	}
}

// keepLogin keeps a login's credentials, unless a newer login of the account began
// meanwhile or it left the config: those would be overwritten by what is now stale.
func (a *Adapter) keepLogin(account Account, gen int, creds Credentials) error {
	a.keeping.Lock()
	defer a.keeping.Unlock()
	if !a.current(account, gen) {
		return errSuperseded
	}
	return saveCredentials(a.secrets, account.Digits, creds)
}

// current reports whether gen is the account's latest login, and it is configured.
func (a *Adapter) current(account Account, gen int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.signIns[account.Name] == gen &&
		slices.ContainsFunc(a.accounts, func(k Account) bool { return k.Name == account.Name && k.Digits == account.Digits })
}

// codeVia is where Telegram sent a code, as a person says it.
func codeVia(t tg.AuthSentCodeTypeClass) string {
	switch t.(type) {
	case *tg.AuthSentCodeTypeApp:
		return "your Telegram app"
	case *tg.AuthSentCodeTypeSMS, *tg.AuthSentCodeTypeSMSWord, *tg.AuthSentCodeTypeSMSPhrase,
		*tg.AuthSentCodeTypeFirebaseSMS, *tg.AuthSentCodeTypeFragmentSMS:
		return "SMS"
	case *tg.AuthSentCodeTypeCall, *tg.AuthSentCodeTypeFlashCall, *tg.AuthSentCodeTypeMissedCall:
		return "a phone call"
	case *tg.AuthSentCodeTypeEmailCode:
		return "email"
	}
	return "Telegram"
}

// describeSendCode is why Telegram would not send a code, as a person can act on it.
func describeSendCode(err error) error {
	switch {
	case tgerr.Is(err, "PHONE_NUMBER_INVALID"):
		return fmt.Errorf("telegram: Telegram does not take this number: %w", err)
	case tgerr.Is(err, "PHONE_NUMBER_BANNED"):
		return fmt.Errorf("telegram: Telegram has banned this number: %w", err)
	case tgerr.Is(err, "API_ID_INVALID", "API_ID_PUBLISHED_FLOOD"):
		return fmt.Errorf("telegram: Telegram refuses the app's api_id and api_hash: %w", err)
	}
	if wait, ok := tgerr.AsFloodWait(err); ok {
		return fmt.Errorf("telegram: Telegram asks to wait %s before another code: %w", wait.Round(time.Second), err)
	}
	return fmt.Errorf("telegram: have the code sent: %w", err)
}

// describeSignIn is why a code did not sign in: a wrong one may be tried again.
func describeSignIn(err error) error {
	var signUp *auth.SignUpRequired
	switch {
	case tgerr.Is(err, "PHONE_CODE_INVALID", "PHONE_CODE_EMPTY"):
		return api.ErrBadCode
	case tgerr.Is(err, "PHONE_CODE_EXPIRED"):
		return fmt.Errorf("telegram: the code expired; have another sent: %w", err)
	case errors.As(err, &signUp):
		return errors.New("telegram: no Telegram account has this number; sign up in a Telegram app first")
	}
	return fmt.Errorf("telegram: sign in: %w", err)
}

// personID is a Telegram user's person ID.
func personID(user int64) string {
	return domain.NativePerson(domain.ProtocolTelegram, strconv.FormatInt(user, 10))
}

// userName is a user as a person calls them.
func userName(u *tg.User) string {
	name := u.FirstName
	if u.LastName != "" {
		name += " " + u.LastName
	}
	if name == "" && u.Username != "" {
		name = "@" + u.Username
	}
	return name
}
