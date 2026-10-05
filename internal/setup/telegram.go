package setup

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
)

// TelegramAccounts checks [[telegram.account]]: each has a name and a phone number,
// and neither repeats (a name picks the account to log in, the number is whom it logs
// in as).
func TelegramAccounts(t config.Telegram) error {
	names, phones := map[string]bool{}, map[string]bool{}
	for i, account := range t.Accounts {
		where := fmt.Sprintf("telegram.account[%d]", i)
		if account.Name == "" {
			return fmt.Errorf("%s: name is empty — `kith login telegram <name>` needs one", where)
		}
		digits := account.Digits()
		if CheckPhone(digits) != nil {
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

// ErrNoTelegramAccount: `kith login telegram` was given a name no account has.
var ErrNoTelegramAccount = errors.New("no such [[telegram.account]]")

// TelegramAccount is the account `kith login telegram [name]` means: the one named, or
// the only one when there is just one.
func TelegramAccount(t config.Telegram, name string) (config.TelegramAccount, error) {
	if name == "" && len(t.Accounts) == 1 {
		return t.Accounts[0], nil
	}
	known := make([]string, 0, len(t.Accounts))
	for _, account := range t.Accounts {
		if account.Name == name {
			return account, nil
		}
		known = append(known, account.Name)
	}
	if name == "" {
		return config.TelegramAccount{}, fmt.Errorf("%w: say which, one of %v", ErrNoTelegramAccount, known)
	}
	return config.TelegramAccount{}, fmt.Errorf("%w named %q (configured: %v)", ErrNoTelegramAccount, name, known)
}

// TelegramName suggests a name for an account: its number's country, as WhatsApp's.
func TelegramName(digits string, taken []string) string {
	if country := PhoneCountry(digits); country != "" {
		return FreeName(country, taken)
	}
	return FreeName("tg"+digits[max(0, len(digits)-4):], taken)
}

// TelegramAppSteps is where a Telegram app's api_id and api_hash come from.
const TelegramAppSteps = "Every Telegram client logs in as an app. Make your own at https://my.telegram.org: " +
	"log in with the account's number, open API development tools, and fill in the form (any title " +
	"and short name; platform Desktop). It shows the app's api_id, a number, and its api_hash. " +
	"They are kept with the session in the system keyring, never in the config."

// TelegramAppID is an api_id as typed: a positive number.
func TelegramAppID(s string) (int, error) {
	id, err := strconv.Atoi(s)
	if err != nil || id <= 0 {
		return 0, errors.New("an api_id is a number, as my.telegram.org shows it")
	}
	return id, nil
}

// CheckTelegramAppHash checks an api_hash as typed: 32 hexadecimal digits.
func CheckTelegramAppHash(s string) error {
	if len(s) != 32 || strings.Trim(strings.ToLower(s), "0123456789abcdef") != "" {
		return errors.New("an api_hash is 32 letters and digits (0-9, a-f), as my.telegram.org shows it")
	}
	return nil
}
