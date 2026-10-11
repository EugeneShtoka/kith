package setup_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// An alias on a number ID reaches every account the directory links to that number.
func TestAnAliasOnANumberReachesTheirOtherAccounts(t *testing.T) {
	t.Parallel()
	aliases := setup.Aliases([]config.Identity{{Alias: "Fay (mine)", IDs: []string{"whatsapp:15550100009@s.whatsapp.net"}}})
	dir := domain.NewDirectory(nil, []domain.PersonLink{{Source: "telegram:1", ID: "telegram:9", Other: domain.PhoneID("15550100009")}})
	if got := setup.PeopleOf(aliases, dir).Name("telegram:9", "Fay Telegram"); got != "Fay (mine)" {
		t.Errorf("her Telegram account = %q, want the alias set on her WhatsApp", got)
	}
}
