package setup_test

import (
	"maps"
	"slices"
	"strings"
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
	people := domain.People{Alias: func(user string) string { return aliases[user] }, Dir: dir}
	if got := people.Name("telegram:9", "Fay Telegram"); got != "Fay (mine)" {
		t.Errorf("her Telegram account = %q, want the alias set on her WhatsApp", got)
	}
}

// An identity's picture stands for each of its IDs in order: the one it names, then
// their own, then the rest as written; identities sharing an alias are one, the first
// picture named winning. A picture none of its IDs is refused.
func TestAnIdentitysPictureChainsItsIDs(t *testing.T) {
	t.Parallel()
	ids := []config.Identity{
		{Alias: "Dana", IDs: []string{"whatsapp:1@s.whatsapp.net", "telegram:2"}},
		{Alias: "Dana", IDs: []string{"slack:T.U3"}, Picture: "telegram:2"},
		{Alias: "Eli", IDs: []string{"telegram:4", "signal:5"}},
	}
	got := setup.FacesOf(ids)
	want := map[string][]string{
		"whatsapp:1@s.whatsapp.net": {"telegram:2", "whatsapp:1@s.whatsapp.net", "slack:T.U3"},
		"telegram:2":                {"telegram:2", "whatsapp:1@s.whatsapp.net", "slack:T.U3"},
		"slack:T.U3":                {"telegram:2", "slack:T.U3", "whatsapp:1@s.whatsapp.net"},
		"telegram:4":                {"telegram:4", "signal:5"},
		"signal:5":                  {"signal:5", "telegram:4"},
	}
	if !maps.EqualFunc(got, want, slices.Equal) {
		t.Errorf("FacesOf = %v, want %v", got, want)
	}
	cfg := config.Config{Profiles: []config.Profile{{Name: "Home", Include: []string{"*"}}}}
	cfg.Display.Identities = ids
	if err := setup.Validate(cfg); err != nil {
		t.Errorf("a picture of the group's refused: %v", err)
	}
	cfg.Display.Identities = []config.Identity{{Alias: "Eli", IDs: []string{"telegram:4"}, Picture: "telegram:9"}}
	if err := setup.Validate(cfg); err == nil || !strings.Contains(err.Error(), "picture") {
		t.Errorf("a picture none of the identity's IDs: %v, want refused", err)
	}
}
