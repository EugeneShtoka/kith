package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
	"go.mau.fi/whatsmeow"
)

// linkSteps tells the user where to enter a pairing code on the phone.
const linkSteps = "On the phone: WhatsApp → Settings → Linked devices → Link a device → " +
	"“Link with phone number instead”, and type the code."

// insteadOfQR is the other way a QR step offers: a code typed on the phone.
const insteadOfQR = "link with a code typed on the phone instead"

// scanSteps tells the user where to scan a QR code from, and for how long it holds.
func scanSteps(holds time.Duration) string {
	return fmt.Sprintf("On the phone: WhatsApp → Settings → Linked devices → Link a device, and scan this code. "+
		"It changes in %d seconds.", int(holds.Round(time.Second).Seconds()))
}

// LoginNetwork describes WhatsApp for login, with its accounts and numbers.
func (a *Adapter) LoginNetwork() api.LoginNetwork {
	n := api.LoginNetwork{Network: "whatsapp", Label: "WhatsApp", Detail: "a phone number, linked as one of its devices"}
	for _, account := range a.accountsNow() {
		n.Accounts = append(n.Accounts, api.LoginAccount{Name: account.Name, Detail: "+" + account.Digits})
	}
	return n
}

// Login links account as a device (setting up a new one when ""): the phone scans a
// QR code, or, when the person takes the other way, accepts a code typed on it.
// Without a store nothing can be linked.
func (a *Adapter) Login(ctx context.Context, account string, talk api.LoginTalk) (api.LoginEnd, error) {
	if a.store == nil {
		return api.LoginEnd{}, errStoreOff
	}
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
	linked, err := a.pair(ctx, account, func(qr <-chan whatsmeow.QRChannelItem, _ *whatsmeow.Client, _ Account) error {
		return showQRs(ctx, qr, talk)
	})
	if errors.Is(err, errInstead) {
		// A pairing of its own: the first one's QR codes would run out, and whatsmeow
		// disconnects then, while the code is still being typed.
		linked, err = a.pair(ctx, account, func(qr <-chan whatsmeow.QRChannelItem, client *whatsmeow.Client, acc Account) error {
			return askForCode(ctx, client, qr, acc.Digits, func(code string) error { return talk.Show(ctx, code, linkSteps) })
		})
	}
	if err != nil {
		return api.LoginEnd{}, err
	}
	return api.LoginEnd{Done: "linked WhatsApp " + account + " as " + linked + " — its chats arrive over the next minutes"}, nil
}

// Accounts is each connected account, by the digits its rooms are under.
func (a *Adapter) Accounts() []domain.Account {
	var out []domain.Account
	for _, c := range a.connected() {
		out = append(out, domain.Account{
			Network: domain.ProtocolWhatsApp, On: domain.AccountRooms(domain.ProtocolWhatsApp, c.account.Digits), Name: c.account.Name,
		})
	}
	return out
}
