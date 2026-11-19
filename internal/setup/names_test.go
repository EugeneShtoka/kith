package setup

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
)

func TestNameRulesRefuseWhatCannotWork(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		rule config.NameRule
		want string
	}{
		{config.NameRule{Match: "space:Work", Strip: `(WA`}, "display.name_rule[0].strip"},
		{config.NameRule{Match: "space:Work", Name: "Sveta"}, "a name needs the sender"},
	} {
		_, err := NameRules(config.Display{NameRules: []config.NameRule{c.rule}})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("NameRules(%+v) = %v, want %q", c.rule, err, c.want)
		}
	}
	rules, err := NameRules(config.Display{FirstNameOnly: true, NameRules: []config.NameRule{
		{Match: "space:Work", Strip: `\s*\(WA\)$`},
		{Sender: "telegram:1", Name: "Sveta"},
	}})
	if err != nil || !rules.FirstNameOnly || len(rules.Rules) != 2 || rules.Rules[0].Strip == nil {
		t.Errorf("NameRules = %+v, %v", rules, err)
	}
}
