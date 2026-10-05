package telegram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Logging in is a conversation (api.LoginTalk): the number is given, Telegram sends a
// code, the code is given, and the password when the account has two-step
// verification on. It runs on a connection of its own, which the session it signs in
// with is taken from.

// ErrNoAccount is a login for a name no [[telegram.account]] has.
var ErrNoAccount = errors.New("telegram: no such [[telegram.account]]")

var (
	// errSuperseded is a login overtaken by a newer one of the same account, or by its
	// leaving the config.
	errSuperseded = errors.New("telegram: another login of this account began meanwhile")
	// errNeedCode is an answer with no code, before the code was taken.
	errNeedCode = errors.New("telegram: the code Telegram sent is needed")
)

// login is an account's login under way: which of its logins it is (see
// Adapter.signIns), and how to end it.
type login struct {
	gen    int
	cancel context.CancelFunc
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

// LoginNetwork is Telegram as a login lists it, with its accounts and their numbers.
func (a *Adapter) LoginNetwork() api.LoginNetwork {
	n := api.LoginNetwork{Network: "telegram", Label: "Telegram", Detail: "a phone number, logged in with a code Telegram sends"}
	for _, account := range a.accountsNow() {
		n.Accounts = append(n.Accounts, api.LoginAccount{Name: account.Name, Detail: "+" + account.Digits})
	}
	return n
}

// Login logs account in, setting it up first when it is new (""): the app to log in
// through, then the code Telegram sends, then the two-step password when the account
// has one. A wrong answer is asked for again.
func (a *Adapter) Login(ctx context.Context, name string, talk api.LoginTalk) (api.LoginEnd, error) {
	if name == "" {
		var err error
		name, err = api.NumberedAccount(ctx, talk, "telegram.account", "tg", a.LoginNetwork().Accounts,
			"The Telegram account's phone number, with its country code: +44 7700 900123.\n\n"+
				"Telegram sends a code to it — to the Telegram app where the account is logged in, else by "+
				"SMS — and tells the account's other sessions about the new login.",
			"What kith calls this account: its chats are the space “Telegram <name>” in the rail, "+
				"and `kith login telegram <name>` logs it in again. Suggested from the number's country; "+
				"enter keeps it.")
		if err != nil {
			return api.LoginEnd{}, err
		}
	}
	account, err := a.account(name)
	if err != nil {
		return api.LoginEnd{}, err
	}
	app, err := askApp(ctx, talk)
	if err != nil {
		return api.LoginEnd{}, err
	}
	return a.login(ctx, account, app, talk, a.newClient)
}

// askApp asks for the app to log in through until one will do: one's own, or, left
// empty, kith's own when this build carries one.
func askApp(ctx context.Context, talk api.LoginTalk) (App, error) {
	note := ""
	for {
		got, err := talk.Ask(ctx, note,
			api.LoginField{Key: "api_id", Label: "api_id", Optional: true, Help: appSteps + "\n\nLeave both empty to " +
				"log in through kith's own app, when this build carries one. Your own is safer: Telegram may limit " +
				"an app many people share."},
			api.LoginField{Key: "api_hash", Label: "api_hash", Optional: true, Secret: true,
				Help: "The app's api_hash, from the same page: 32 letters and digits."})
		if err != nil {
			return App{}, err
		}
		given := App{Hash: strings.TrimSpace(got["api_hash"])}
		if raw := strings.TrimSpace(got["api_id"]); raw != "" {
			if given.ID, err = strconv.Atoi(raw); err != nil || given.ID <= 0 {
				note = "an api_id is a number, as my.telegram.org shows it"
				continue
			}
		}
		if given.Hash != "" && !isHash(given.Hash) {
			note = "an api_hash is 32 letters and digits (0-9, a-f), as my.telegram.org shows it"
			continue
		}
		app, err := appFor(given)
		if err != nil {
			note = err.Error()
			continue
		}
		return app, nil
	}
}

// appSteps is where a Telegram app's api_id and api_hash come from.
const appSteps = "Every Telegram client logs in as an app. Make your own at https://my.telegram.org: " +
	"log in with the account's number, open API development tools, and fill in the form (any title " +
	"and short name; platform Desktop). It shows the app's api_id, a number, and its api_hash. " +
	"They are kept with the session in the system keyring, never in the config."

// isHash reports whether s is an api_hash: 32 hexadecimal digits.
func isHash(s string) bool {
	return len(s) == 32 && strings.Trim(strings.ToLower(s), "0123456789abcdef") == ""
}

// login logs account in through dial and app, ending any login of it under way: the
// code is asked for once Telegram sent it, and the password when one is needed.
// Signed in, the credentials are kept and the account connects.
func (a *Adapter) login(ctx context.Context, account Account, app App, talk api.LoginTalk, dial dialer) (api.LoginEnd, error) {
	ctx, gen := a.beginLogin(ctx, account)
	defer a.endLogin(account.Name, gen)
	storage := &session.StorageMemory{}
	client := dial(app, storage)
	var end api.LoginEnd
	err := client.Run(ctx, func(ctx context.Context) error {
		phone := "+" + account.Digits
		code, err := client.Auth().SendCode(ctx, phone, auth.SendCodeOptions{})
		if err != nil {
			return describeSendCode(err)
		}
		sent, ok := code.(*tg.AuthSentCode)
		if !ok {
			return errors.New("telegram: logged in without a code, which kith does not ask for")
		}
		note, codeTaken := "Telegram sent a code to "+codeVia(sent.Type), false
		for {
			field := api.LoginField{Key: "code", Label: "code", Help: "The code Telegram sent. It reaches the Telegram " +
				"app where the account is logged in, as a message from Telegram, or comes by SMS. Never give it to anyone."}
			if codeTaken {
				field = api.LoginField{Key: "password", Label: "password", Secret: true, Help: "The account's two-step " +
					"verification password (Telegram → Settings → Privacy and Security). It is checked by Telegram " +
					"and not kept."}
			}
			got, err := talk.Ask(ctx, note, field)
			if err != nil {
				return err
			}
			user, err := a.signIn(ctx, client, phone, sent.PhoneCodeHash, got["code"], got["password"], &codeTaken)
			switch {
			case errors.Is(err, api.ErrBadCode):
				note = "that is not the code Telegram sent; try again"
			case errors.Is(err, errNeedCode):
				note = "the code Telegram sent is needed"
			case errors.Is(err, api.ErrPasswordNeeded):
				note = "the account has two-step verification"
			case errors.Is(err, api.ErrBadPassword):
				note = "that is not the password; try again"
			case err != nil:
				return err
			default:
				creds := Credentials{App: app, User: user.ID}
				if creds.Session, err = storage.Bytes(nil); err != nil {
					return fmt.Errorf("telegram: read the session: %w", err)
				}
				if err := a.keepLogin(account, gen, creds); err != nil {
					return err
				}
				a.connectAs(account, creds, gen, dial)
				end.Done = "logged in to Telegram as " + userName(user) + " — its chats arrive over the next minutes"
				return nil
			}
		}
	})
	return end, err //nolint:wrapcheck // its own words, or the login's end
}

// beginLogin makes a login of account the latest, ending one under way, and is its
// context and generation.
func (a *Adapter) beginLogin(ctx context.Context, account Account) (context.Context, int) {
	ctx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	if old := a.logins[account.Name]; old != nil {
		old.cancel()
	}
	a.signIns[account.Name]++
	gen := a.signIns[account.Name]
	a.logins[account.Name] = &login{gen: gen, cancel: cancel}
	return ctx, gen
}

// endLogin forgets a login, unless a newer one replaced it.
func (a *Adapter) endLogin(name string, gen int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if l := a.logins[name]; l != nil && l.gen == gen {
		l.cancel()
		delete(a.logins, name)
	}
}

// signIn tries one answer: the code, then the password once Telegram asked for one
// (codeTaken). The user is whom it signed in as.
func (a *Adapter) signIn(
	ctx context.Context, client *telegram.Client, phone, hash, code, password string, codeTaken *bool,
) (*tg.User, error) {
	if !*codeTaken {
		if code == "" {
			return nil, errNeedCode
		}
		in, err := client.Auth().SignIn(ctx, phone, code, hash)
		switch {
		case errors.Is(err, auth.ErrPasswordAuthNeeded):
			*codeTaken = true
		case err != nil:
			return nil, describeSignIn(err)
		default:
			return signedInAs(in)
		}
	}
	if password == "" {
		return nil, api.ErrPasswordNeeded
	}
	in, err := client.Auth().Password(ctx, password)
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
