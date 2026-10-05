package api

import (
	"context"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Logging in is a conversation each network leads: it asks for what it needs, shows
// what to do with a code, has a new account written into the config, and ends saying
// whom it logged in as. A client draws each step and answers it, knowing no network:
// `:login` and `kith login` are one driver each, whatever the network.

// LoginField is one thing a login asks for.
type LoginField struct {
	// Key names the answer; Label is what the prompt says.
	Key, Label string
	// Help is what it is and where to find it, as paragraphs (an indented line is
	// kept as written).
	Help string
	// Value is a likely answer, offered to keep (a name from the number's country).
	Value string
	// Secret is drawn as dots and read without echo; Optional takes an empty answer.
	Secret, Optional bool
}

// LoginRecord is a new account for the client to write into the config: a record of
// Table ("whatsapp.account") with Values by field, or, with no Table, Values by key
// path (Matrix's "homeserver" and "user").
type LoginRecord struct {
	Table  string
	Values map[string]string
}

// LoginStep is where a login is: what to ask, show or write, or that it ended.
// Exactly one of Ask, Code, Configure, Restart and Done says what to do; Note may
// accompany any of them.
type LoginStep struct {
	// Login is the conversation, for AnswerLogin and CancelLogin.
	Login string
	// Account is the account it is about, once known (a new one has a name only
	// after it was asked).
	Account string
	// Note is what just happened: why an answer was refused, where a code went.
	Note string
	// Ask is fields to answer, all at once, with AnswerLogin.
	Ask []LoginField
	// Code is to be shown until the next step: Note says where to type it.
	// AnswerLogin, with no values, waits for what comes after.
	Code string
	// Configure is a new account to write into the config; once written and the
	// daemon has re-read it, AnswerLogin, with no values, goes on.
	Configure *LoginRecord
	// Restart ends the conversation: the daemon must restart to go on, after which
	// BeginLogin on Account begins it again.
	Restart bool
	// Done ends the conversation: what the login did.
	Done string
}

// LoginNetwork is a network that can be logged in to, and its accounts set up already.
type LoginNetwork struct {
	// Network is what BeginLogin takes ("whatsapp"); Label is its name, Detail what
	// logging in to it takes.
	Network, Label, Detail string
	Accounts               []LoginAccount
}

// LoginAccount is an account set up already: its name and what it is (a number, a
// workspace).
type LoginAccount struct {
	Name, Detail string
}

// Logins logs network accounts in. Only the daemon logs in, because it keeps the
// sessions and connects on them; `:login` and `kith login` ask it.
type Logins interface {
	// LoginNetworks is every network the daemon can log in to.
	LoginNetworks(ctx context.Context) ([]LoginNetwork, error)
	// BeginLogin starts logging account in ("" sets a new one up), ending any login of
	// it under way, and is the first step.
	BeginLogin(ctx context.Context, network, account string) (LoginStep, error)
	// AnswerLogin answers the step the login is at (values by field key; none for a
	// code shown or a record written) and is the next step. A failure ends the login.
	AnswerLogin(ctx context.Context, login string, values map[string]string) (LoginStep, error)
	// CancelLogin ends a login.
	CancelLogin(ctx context.Context, login string) error
}

// LoginTalk is a login's side of the conversation, as the network leads it.
type LoginTalk interface {
	// Ask asks for fields and is the answers, by key; note says why (a refused
	// answer). It waits for them.
	Ask(ctx context.Context, note string, fields ...LoginField) (map[string]string, error)
	// Show has the client show code, note saying where to type it, until the next
	// step. It does not wait.
	Show(ctx context.Context, code, note string) error
	// Configure has the client write a new account into the config, and waits until
	// the daemon has re-read it.
	Configure(ctx context.Context, record LoginRecord) error
	// Named says which account the login is about, once a new one has a name.
	Named(account string)
}

// LoginEnd is how a login ended: Done says what it did; Restart, that the daemon
// must restart to go on (a new Matrix account, whose store opens at start).
type LoginEnd struct {
	Done    string
	Restart bool
}

// LoginLeader is a network that leads its own login.
type LoginLeader interface {
	// LoginNetwork is the network as a login lists it, with its accounts.
	LoginNetwork() LoginNetwork
	// Login logs account in ("" sets a new one up), talking through talk, until it
	// is done, fails, or ctx ends. A wrong answer is asked again, with a note.
	Login(ctx context.Context, account string, talk LoginTalk) (LoginEnd, error)
}

// NumberedAccount sets up a new account of a network that knows people by number
// (WhatsApp, Telegram): it asks for the number, then a name (suggested from the
// number's country, prefix and its last digits otherwise), refusing a number or name
// one of accounts has, and has the client write it into table. help is the number's
// help, and the name's.
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
