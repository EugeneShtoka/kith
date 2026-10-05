package domain

import "fmt"

// AccountRecord is what CheckAccounts reads of one configured account: its name, and
// the field that says which account it is on its network (a phone number, a
// workspace) as written, as compared, and why it names no account (nil when it does).
type AccountRecord struct {
	Name, Field, Written, Key string
	Bad                       error
}

// CheckAccounts refuses a record table's accounts with no name, with a field naming
// no account, or repeating another's name or account: a name picks the account to log
// in, and the field is whom it logs in as. table names the records in the error
// ("whatsapp.account[1]").
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

// PhoneAccount is the record of an account a phone number names (WhatsApp's,
// Telegram's).
func PhoneAccount(name, phone string) AccountRecord {
	digits := PhoneDigits(phone)
	a := AccountRecord{Name: name, Field: "phone", Written: phone, Key: digits}
	if err := CheckPhone(digits); err != nil {
		a.Bad = fmt.Errorf("is not an international number — %w", err)
	}
	return a
}
