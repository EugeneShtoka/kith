package setup

import (
	"errors"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/config"
)

// WhatsAppAccounts checks [[whatsapp.account]]: each has a name and a phone number,
// and neither repeats (a name picks the account to log in, the number names its
// rooms).
func WhatsAppAccounts(w config.WhatsApp) error {
	names, phones := map[string]bool{}, map[string]bool{}
	for i, account := range w.Accounts {
		where := fmt.Sprintf("whatsapp.account[%d]", i)
		if account.Name == "" {
			return fmt.Errorf("%s: name is empty — `kith login whatsapp <name>` needs one", where)
		}
		digits := account.Digits()
		if len(digits) < 7 || len(digits) > 15 {
			return fmt.Errorf("%s (%s): phone %q is not an international number", where, account.Name, account.Phone)
		}
		if names[account.Name] {
			return fmt.Errorf("%s: name %q is used twice", where, account.Name)
		}
		if phones[digits] {
			return fmt.Errorf("%s (%s): phone %s is listed twice", where, account.Name, account.Phone)
		}
		names[account.Name], phones[digits] = true, true
	}
	return nil
}

// ErrNoWhatsAppAccount: `kith login whatsapp` was given a name no account has.
var ErrNoWhatsAppAccount = errors.New("no such [[whatsapp.account]]")

// WhatsAppAccount is the account `kith login whatsapp [name]` means: the one named,
// or the only one when there is just one.
func WhatsAppAccount(w config.WhatsApp, name string) (config.WhatsAppAccount, error) {
	if name == "" && len(w.Accounts) == 1 {
		return w.Accounts[0], nil
	}
	known := make([]string, 0, len(w.Accounts))
	for _, account := range w.Accounts {
		if account.Name == name {
			return account, nil
		}
		known = append(known, account.Name)
	}
	if name == "" {
		return config.WhatsAppAccount{}, fmt.Errorf("%w: say which, one of %v", ErrNoWhatsAppAccount, known)
	}
	return config.WhatsAppAccount{}, fmt.Errorf("%w named %q (configured: %v)", ErrNoWhatsAppAccount, name, known)
}
