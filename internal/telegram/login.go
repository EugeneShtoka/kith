package telegram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Login is an api.LoginTalk: a QR code scanned (or the number's code), then the 2FA
// password if set. It runs on its own connection, whose session becomes the account's.

// ErrNoAccount is a login for a name no [[telegram.account]] has.
var ErrNoAccount = errors.New("telegram: no such [[telegram.account]]")

var (
	// errSuperseded: a newer login of the account began, or it left the config.
	errSuperseded = errors.New("telegram: another login of this account began meanwhile")
	// errNeedCode is an empty answer to the code prompt.
	errNeedCode = errors.New("telegram: the code Telegram sent is needed")
	// errPasswordNeeded: the account has 2FA on.
	errPasswordNeeded = errors.New("telegram: this account has a two-step verification password; give it too")
	// errBadCode is a wrong code; the person may retry.
	errBadCode = errors.New("telegram: that is not the code Telegram sent")
	// errInstead: the person took the code Telegram sends over the QR code.
	errInstead = errors.New("telegram: logging in with a code instead")
	// errOtherAccount: the QR code was scanned with another account's phone.
	errOtherAccount = errors.New("telegram: the QR code was scanned with another Telegram account")
)

// login is a login in progress: its generation (Adapter.signIns) and cancel.
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

// LoginNetwork is Telegram with its accounts and numbers, for the login picker.
func (a *Adapter) LoginNetwork() api.LoginNetwork {
	n := api.LoginNetwork{Network: "telegram", Label: "Telegram", Detail: "a phone number, logged in with a code Telegram sends"}
	for _, account := range a.accountsNow() {
		n.Accounts = append(n.Accounts, api.LoginAccount{Name: account.Name, Detail: "+" + account.Digits})
	}
	return n
}

// Login logs account in ("" sets up a new one): app, code, then 2FA password if any.
// Wrong answers are asked again.
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

// askApp asks for the api_id/api_hash until valid; empty means the build's own app,
// if it has one.
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

// login runs one login, canceling any earlier one of account: a QR code the phone
// scans, or, when the person takes the other way, a code Telegram sends; then the 2FA
// password if set. Once signed in as the account's number it stores the credentials
// and connects.
func (a *Adapter) login(ctx context.Context, account Account, app App, talk api.LoginTalk, dial dialer) (api.LoginEnd, error) {
	ctx, gen := a.beginLogin(ctx, account)
	defer a.endLogin(account.Name, gen)
	storage := newLoginSession()
	updates := tg.NewUpdateDispatcher()
	scanned := qrlogin.OnLoginToken(updates)
	client := dial(app, storage, updates)
	var end api.LoginEnd
	err := client.Run(ctx, func(ctx context.Context) error {
		user, err := scanIn(ctx, client, scanned, talk)
		if errors.Is(err, errInstead) {
			user, err = a.codeIn(ctx, client, "+"+account.Digits, talk)
		}
		if errors.Is(err, errPasswordNeeded) {
			user, err = a.passwordIn(ctx, client, talk)
		}
		if err != nil {
			return err
		}
		if domain.PhoneDigits(user.Phone) != account.Digits {
			// Scanned from another account's phone: end that session, keep nothing.
			_, _ = client.API().AuthLogOut(ctx)
			return fmt.Errorf("%w: it is +%s, not %s's +%s", errOtherAccount, user.Phone, account.Name, account.Digits)
		}
		creds := Credentials{App: app, User: user.ID}
		if creds.Session, err = storage.saved(ctx); err != nil {
			return err
		}
		if err := a.keepLogin(account, gen, creds); err != nil {
			return err
		}
		a.connectAs(account, creds, gen, dial)
		end.Done = "logged in to Telegram as " + userName(user) + " — its chats arrive over the next minutes"
		return nil
	})
	if err == nil && end.Done == "" {
		// gotd's Run ends quietly when ctx does, whatever the login had reached.
		err = fmt.Errorf("telegram: the login ended before signing in: %w", context.Cause(ctx))
	}
	return end, err
}

// insteadOfQR is the other way a QR step offers: a code Telegram sends.
const insteadOfQR = "log in with a code Telegram sends instead"

// scanSteps tells the user where to scan a QR code from, and for how long it holds.
func scanSteps(holds time.Duration) string {
	return fmt.Sprintf("On the phone: Telegram → Settings → Devices → Link Desktop Device, and scan this code. "+
		"It changes in %d seconds.", max(int(holds.Round(time.Second).Seconds()), 1))
}

// scanIn logs in by a QR code a phone logged in to the account scans, a new one each
// time the last expires. It returns errInstead once the person takes the code Telegram
// sends instead, and errPasswordNeeded when the account has 2FA on.
func scanIn(ctx context.Context, client *telegram.Client, scanned qrlogin.LoggedIn, talk api.LoginTalk) (*tg.User, error) {
	scan, stop := context.WithCancel(ctx)
	defer stop()
	took := make(chan struct{})
	go func() {
		select {
		case <-talk.Instead():
			close(took)
			stop()
		case <-scan.Done():
		}
	}()
	in, err := client.QR().Auth(scan, scanned, func(ctx context.Context, token qrlogin.Token) error {
		return talk.ShowQR(ctx, token.URL(), scanSteps(time.Until(token.Expires())), insteadOfQR)
	})
	switch {
	case err == nil:
		return signedInAs(in)
	case tgerr.Is(err, "SESSION_PASSWORD_NEEDED"):
		return nil, errPasswordNeeded
	}
	select {
	case <-took:
		return nil, errInstead
	default:
	}
	return nil, fmt.Errorf("telegram: log in by QR code: %w", err)
}

// codeIn logs in by a code Telegram sends to phone, asked again while wrong. It
// returns errPasswordNeeded when the account has 2FA on.
func (a *Adapter) codeIn(ctx context.Context, client *telegram.Client, phone string, talk api.LoginTalk) (*tg.User, error) {
	code, err := client.Auth().SendCode(ctx, phone, auth.SendCodeOptions{})
	if err != nil {
		return nil, describeSendCode(err)
	}
	sent, ok := code.(*tg.AuthSentCode)
	if !ok {
		return nil, errors.New("telegram: logged in without a code, which kith does not ask for")
	}
	note := "Telegram sent a code to " + codeVia(sent.Type)
	for {
		got, err := talk.Ask(ctx, note, api.LoginField{Key: "code", Label: "code", Help: "The code Telegram sent. It reaches the Telegram " +
			"app where the account is logged in, as a message from Telegram, or comes by SMS. Never give it to anyone."})
		if err != nil {
			return nil, err
		}
		codeTaken := false
		user, err := a.signIn(ctx, client, phone, sent.PhoneCodeHash, got["code"], "", &codeTaken)
		switch {
		case codeTaken:
			return nil, errPasswordNeeded
		case errors.Is(err, errBadCode):
			note = "that is not the code Telegram sent; try again"
		case errors.Is(err, errNeedCode):
			note = "the code Telegram sent is needed"
		case err != nil:
			return nil, err
		default:
			return user, nil
		}
	}
}

// passwordIn finishes a login of an account with 2FA on, asking for its password
// again while wrong.
func (a *Adapter) passwordIn(ctx context.Context, client *telegram.Client, talk api.LoginTalk) (*tg.User, error) {
	note := "the account has two-step verification"
	for {
		got, err := talk.Ask(ctx, note, api.LoginField{Key: "password", Label: "password", Secret: true, Help: "The account's two-step " +
			"verification password (Telegram → Settings → Privacy and Security). It is checked by Telegram " +
			"and not kept."})
		if err != nil {
			return nil, err
		}
		codeTaken := true
		user, err := a.signIn(ctx, client, "", "", "", got["password"], &codeTaken)
		switch {
		case errors.Is(err, errPasswordNeeded):
			note = "the account has two-step verification"
		case errors.Is(err, api.ErrBadPassword):
			note = "that is not the password; try again"
		case err != nil:
			return nil, err
		default:
			return user, nil
		}
	}
}

// beginLogin starts a new login generation, canceling any in progress.
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

// signIn tries one answer: the code, or the password once Telegram asked (codeTaken).
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
		return nil, errPasswordNeeded
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

// keepLogin stores credentials unless a newer login began or the account left the
// config.
func (a *Adapter) keepLogin(account Account, gen int, creds Credentials) error {
	a.keeping.Lock()
	defer a.keeping.Unlock()
	if !a.current(account, gen) {
		return errSuperseded
	}
	return saveCredentials(a.secrets, account.Digits, creds)
}

// current reports whether gen is the latest login of a still-configured account.
func (a *Adapter) current(account Account, gen int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.signIns[account.Name] == gen &&
		slices.ContainsFunc(a.accounts, func(k Account) bool { return k.Name == account.Name && k.Digits == account.Digits })
}

// codeVia describes where Telegram sent the code.
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

// describeSendCode explains a failed code request in actionable terms.
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

// describeSignIn explains a failed sign-in; a wrong code may be retried.
func describeSignIn(err error) error {
	var signUp *auth.SignUpRequired
	switch {
	case tgerr.Is(err, "PHONE_CODE_INVALID", "PHONE_CODE_EMPTY"):
		return errBadCode
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

// userName is a user's full name, else their @username.
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

// loginSession holds a login's session in memory until stored. gotd stores it after
// connection setup, which can finish after sign-in, so the login waits for it.
type loginSession struct {
	session.StorageMemory
	stored chan struct{}
	once   sync.Once
}

func newLoginSession() *loginSession { return &loginSession{stored: make(chan struct{})} }

// StoreSession keeps data and signals saved.
func (s *loginSession) StoreSession(ctx context.Context, data []byte) error {
	if err := s.StorageMemory.StoreSession(ctx, data); err != nil {
		return err //nolint:wrapcheck // gotd's own storage
	}
	s.once.Do(func() { close(s.stored) })
	return nil
}

// saved is the session once gotd has stored it.
func (s *loginSession) saved(ctx context.Context) ([]byte, error) {
	select {
	case <-s.stored:
	case <-ctx.Done():
		return nil, fmt.Errorf("telegram: the session was never stored: %w", ctx.Err())
	}
	data, err := s.Bytes(nil)
	if err != nil {
		return nil, fmt.Errorf("telegram: read the session: %w", err)
	}
	return data, nil
}

// Accounts is each connected account, by the user ID its rooms are under.
func (a *Adapter) Accounts() []domain.Account {
	var out []domain.Account
	for _, c := range a.connected() {
		out = append(out, domain.Account{
			Network: domain.ProtocolTelegram, On: domain.AccountRooms(domain.ProtocolTelegram, strconv.FormatInt(c.user, 10)),
			Name: c.account.Name,
		})
	}
	slices.SortFunc(out, func(x, y domain.Account) int { return strings.Compare(x.Name, y.Name) })
	return out
}
