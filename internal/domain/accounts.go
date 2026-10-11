package domain

import "fmt"

// AccountRecord is one configured account for CheckAccounts: its name, and the field
// identifying it on its network (e.g. phone) as written, as compared (Key), and why
// it is invalid (Bad, nil when valid).
type AccountRecord struct {
	Name, Field, Written, Key string
	Bad                       error
}

// CheckAccounts rejects accounts with no name, an invalid field, or a duplicate name
// or account. table prefixes the error ("whatsapp.account[1]").
func CheckAccounts(table string, accounts []AccountRecord) error {
	names, keys := map[string]bool{}, map[string]bool{}
	for i, a := range accounts {
		where := fmt.Sprintf("%s[%d]", table, i)
		if a.Name == "" {
			return fmt.Errorf("%s: name is empty — `kith login` names the account by it", where)
		}
		if a.Bad != nil {
			return fmt.Errorf("%s (%s): %s %q %w", where, a.Name, a.Field, a.Written, a.Bad)
		}
		if names[a.Name] {
			return fmt.Errorf("%s: name %q is used twice", where, a.Name)
		}
		if keys[a.Key] {
			return fmt.Errorf("%s (%s): %s %s is listed twice", where, a.Name, a.Field, a.Written)
		}
		names[a.Name], keys[a.Key] = true, true
	}
	return nil
}

// PhoneAccount is the record of a phone-numbered account (WhatsApp, Telegram).
func PhoneAccount(name, phone string) AccountRecord {
	digits := PhoneDigits(phone)
	a := AccountRecord{Name: name, Field: "phone", Written: phone, Key: digits}
	if err := CheckPhone(digits); err != nil {
		a.Bad = fmt.Errorf("is not an international number — %w", err)
	}
	return a
}
