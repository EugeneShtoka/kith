package setup

import (
	"fmt"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
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
		if domain.CheckPhone(digits) != nil {
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
