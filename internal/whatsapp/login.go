package whatsapp

import (
	"context"

	"github.com/EugeneShtoka/kith/internal/api"
)

// linkSteps is what to do on the phone with a pairing code.
const linkSteps = "On the phone: WhatsApp → Settings → Linked devices → Link a device → " +
	"“Link with phone number instead”, and type the code."

// LoginNetwork is WhatsApp as a login lists it, with its accounts and their numbers.
func (a *Adapter) LoginNetwork() api.LoginNetwork {
	n := api.LoginNetwork{Network: "whatsapp", Label: "WhatsApp", Detail: "a phone number, linked as one of its devices"}
	for _, account := range a.accountsNow() {
		n.Accounts = append(n.Accounts, api.LoginAccount{Name: account.Name, Detail: "+" + account.Digits})
	}
	return n
}

// Login links account as one of its phone's devices, setting it up first when it is
// new (""): the code WhatsApp gives is shown until the phone accepts it.
func (a *Adapter) Login(ctx context.Context, account string, talk api.LoginTalk) (api.LoginEnd, error) {
	if account == "" {
		name, err := api.NumberedAccount(ctx, talk, "whatsapp.account", "wa", a.LoginNetwork().Accounts,
			"The WhatsApp account's phone number, with its country code: +44 7700 900123.\n\n"+
				"kith becomes one of the phone's linked devices, as WhatsApp Web is. The phone must come "+
				"online every couple of weeks, or WhatsApp unlinks it. Linking asks the phone for all the "+
				"history it holds; it arrives over the minutes after.",
			"What kith calls this account: its rooms are the space “WhatsApp <name>” in the rail, "+
				"and `kith login whatsapp <name>` links it again. Suggested from the number's country; "+
				"enter keeps it.")
		if err != nil {
			return api.LoginEnd{}, err
		}
		account = name
	}
	linked, err := a.pair(ctx, account, func(code string) error { return talk.Show(ctx, code, linkSteps) })
	if err != nil {
		return api.LoginEnd{}, err
	}
	return api.LoginEnd{Done: "linked WhatsApp " + account + " as " + linked + " — its chats arrive over the next minutes"}, nil
}
