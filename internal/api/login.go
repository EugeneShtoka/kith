package api

import (
	"context"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Login is a step-by-step conversation the network leads (ask, show a code, write
// config, done); clients draw and answer steps without knowing the network.

// LoginField is one thing a login asks for.
type LoginField struct {
	// Key names the answer; Label is what the prompt says.
	Key, Label string
	// Help is paragraphs explaining the field; an indented line is kept verbatim.
	Help string
	// Value is a suggested answer.
	Value string
	// Secret is drawn as dots and read without echo; Optional takes an empty answer.
	Secret, Optional bool
}

// LoginRecord is a new account for the client to write into the config: a Table
// record ("whatsapp.account") by field, or with no Table, Values by key path.
type LoginRecord struct {
	Table  string
	Values map[string]string
}

// LoginStep is the current login step. Exactly one of Ask, Code, QR, Configure,
// Restart and Done is set; Note may accompany any.
type LoginStep struct {
	// Login identifies the conversation for AnswerLogin and CancelLogin.
	Login string
	// Account is the account, once known.
	Account string
	// Note is what just happened, e.g. why an answer was refused.
	Note string
	// Ask is fields to answer together via AnswerLogin.
	Ask []LoginField
	// Code is shown until the next step (Note says where to type it); AnswerLogin
	// with no values waits for it.
	Code string
	// QR is data to draw as a QR code until the next step, for a phone to scan (Note
	// says where); AnswerLogin with no values waits for the scan.
	QR string
	// Instead, with QR, is another way to log in, said as the choice to offer ("link
	// with a code typed on the phone instead"); InsteadLogin takes it.
	Instead string
	// Configure is an account to write into the config; after the daemon re-reads
	// it, AnswerLogin with no values continues.
	Configure *LoginRecord
	// Restart ends the conversation: restart the daemon, then BeginLogin on Account.
	Restart bool
	// Done ends the conversation, saying what the login did.
	Done string
}

// LoginNetwork is a network that can be logged in to, with its existing accounts.
type LoginNetwork struct {
	// Network is what BeginLogin takes ("whatsapp"); Label is its name, Detail what
	// logging in to it takes.
	Network, Label, Detail string
	Accounts               []LoginAccount
}

// LoginAccount is an existing account; Detail is e.g. its number or workspace.
type LoginAccount struct {
	Name, Detail string
}

// AccountStates reports each network account the daemon serves, logged in or not,
// with its phase and why it is down, so a client can show which have stopped.
type AccountStates interface {
	AccountStates(ctx context.Context) ([]domain.AccountStatus, error)
}

// Logins logs network accounts in. Only the daemon logs in, since it keeps the
// sessions; `:login` and `kith login` ask it.
type Logins interface {
	// LoginNetworks is every network the daemon can log in to.
	LoginNetworks(ctx context.Context) ([]LoginNetwork, error)
	// BeginLogin starts logging account in ("" sets up a new one), ending any login of
	// it under way, and returns the first step.
	BeginLogin(ctx context.Context, network, account string) (LoginStep, error)
	// AnswerLogin answers the current step (values by field key; none after a code or
	// record) and returns the next. A failure ends the login.
	AnswerLogin(ctx context.Context, login string, values map[string]string) (LoginStep, error)
	// CancelLogin ends a login.
	CancelLogin(ctx context.Context, login string) error
	// InsteadLogin takes the other way the login's QR step offers. It does not wait:
	// the AnswerLogin waiting on that step returns where the other way leads.
	InsteadLogin(ctx context.Context, login string) error
}

// LoginTalk is a login's side of the conversation, as the network leads it.
type LoginTalk interface {
	// Ask asks for fields and waits for the answers by key; note explains, e.g. a
	// refused answer.
	Ask(ctx context.Context, note string, fields ...LoginField) (map[string]string, error)
	// Show displays code until the next step; note says where to type it. It does
	// not wait.
	Show(ctx context.Context, code, note string) error
	// ShowQR draws data as a QR code until the next step; note says what scans it. It
	// does not wait. instead, when not "", offers another way to log in, said as the
	// choice; Instead is closed once the client takes it.
	ShowQR(ctx context.Context, data, note, instead string) error
	// Instead is closed once the client takes the other way a QR step offered.
	Instead() <-chan struct{}
	// Configure has the client write a new account into the config and waits until
	// the daemon re-reads it.
	Configure(ctx context.Context, record LoginRecord) error
	// Named says which account the login is about, once a new one has a name.
	Named(account string)
}

// LoginEnd is how a login ended. Restart means the daemon must restart (a new Matrix
// account's store opens at start).
type LoginEnd struct {
	Done    string
	Restart bool
}

// LoginLeader is a network that leads its own login.
type LoginLeader interface {
	// LoginNetwork is the network as a login lists it, with its accounts.
	LoginNetwork() LoginNetwork
	// Login logs account in ("" sets up a new one) through talk until done, failed or
	// ctx ends. A wrong answer is asked again with a note.
	Login(ctx context.Context, account string, talk LoginTalk) (LoginEnd, error)
}

// NumberedAccount sets up a new account on a phone-number network (WhatsApp,
// Telegram): it asks for a number and a name, refusing ones accounts already have,
// and has the client write it into table.
func NumberedAccount(
	ctx context.Context, talk LoginTalk, table, prefix string, accounts []LoginAccount, numberHelp, nameHelp string,
) (string, error) {
	var phone, note string
	for {
		got, err := talk.Ask(ctx, note, LoginField{Key: "phone", Label: "phone", Help: numberHelp})
		if err != nil {
			return "", err
		}
		phone = strings.TrimSpace(got["phone"])
		digits := domain.PhoneDigits(phone)
		if err := domain.CheckPhone(digits); err != nil {
			note = err.Error()
			continue
		}
		if i := slices.IndexFunc(accounts, func(a LoginAccount) bool { return domain.PhoneDigits(a.Detail) == digits }); i >= 0 {
			note = "that number is the account " + accounts[i].Name + " already; log it in from the list"
			continue
		}
		break
	}
	if !strings.HasPrefix(phone, "+") {
		phone = "+" + phone
	}
	taken := make([]string, 0, len(accounts))
	for _, a := range accounts {
		taken = append(taken, a.Name)
	}
	name, err := AskName(ctx, talk, domain.PhoneName(domain.PhoneDigits(phone), prefix, taken), taken, nameHelp)
	if err != nil {
		return "", err
	}
	if err := talk.Configure(ctx, LoginRecord{Table: table, Values: map[string]string{"name": name, "phone": phone}}); err != nil {
		return "", err
	}
	talk.Named(name)
	return name, nil
}

// AskName asks what a new account is called: one word, none of taken, suggested.
func AskName(ctx context.Context, talk LoginTalk, suggested string, taken []string, help string) (string, error) {
	note := ""
	for {
		got, err := talk.Ask(ctx, note, LoginField{Key: "name", Label: "call it", Help: help, Value: suggested})
		if err != nil {
			return "", err
		}
		name := strings.TrimSpace(got["name"])
		switch {
		case name == "":
			note = "a name is needed"
		case strings.ContainsAny(name, " \t"):
			note = "a name is one word"
		case slices.Contains(taken, name):
			note = "an account is called " + name + " already"
		default:
			return name, nil
		}
	}
}
