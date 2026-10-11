package setup_test

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// Tri-state bools keep "absent" distinct from "false".
func TestCodeRulesFromConfig(t *testing.T) {
	t.Parallel()

	rules, err := setup.CodeRules(config.Codes{})
	if err != nil {
		t.Fatal(err)
	}
	if rules != domain.DefaultCodeRules() {
		t.Errorf("an empty section gave %+v, want the defaults", rules)
	}
	off := false
	rules, err = setup.CodeRules(config.Codes{MinLength: 6, MaxLength: 6, Symbols: "-", RequireDigit: &off})
	if err != nil {
		t.Fatal(err)
	}
	want := domain.CodeRules{MinLength: 6, MaxLength: 6, Letters: true, Digits: true, Symbols: "-"}
	if rules != want {
		t.Errorf("rules = %+v, want %+v", rules, want)
	}
}

// A shape that can never match is refused at startup.
func TestCodeRulesRejectAnImpossibleShape(t *testing.T) {
	t.Parallel()

	no := false
	if _, err := setup.CodeRules(config.Codes{Letters: &no, Digits: &no}); err == nil {
		t.Error("a shape allowing no characters at all should be rejected")
	}
	if err := setup.Validate(config.Config{Codes: config.Codes{Letters: &no, Digits: &no}}); err == nil {
		t.Error("Validate should carry the same refusal, so main can stop early")
	}
}

// The scope is carried through verbatim.
func TestCodeScopeFromConfig(t *testing.T) {
	t.Parallel()

	scope := setup.CodeScope(config.Codes{Include: []string{"Bridges"}, Exclude: []string{"!noisy:x"}})
	if len(scope.Include) != 1 || scope.Include[0] != "Bridges" {
		t.Errorf("include = %v, want the configured list", scope.Include)
	}
	if len(scope.Exclude) != 1 || scope.Exclude[0] != "!noisy:x" {
		t.Errorf("exclude = %v, want the configured list", scope.Exclude)
	}
}

func TestAutoCopyUnavailable(t *testing.T) {
	t.Parallel()

	working := config.Config{
		Clipboard: config.Clipboard{Command: "wl-copy", AutoCopy: true},
		Codes:     config.Codes{Include: []string{"Bridges"}},
	}
	if err := setup.AutoCopyUnavailable(working); err != nil {
		t.Errorf("a complete configuration should be usable: %v", err)
	}

	// Off is not a problem.
	off := working
	off.Clipboard.AutoCopy = false
	off.Clipboard.Command = ""
	if err := setup.AutoCopyUnavailable(off); err != nil {
		t.Errorf("auto-copy switched off is not a misconfiguration: %v", err)
	}

	noCommand := working
	noCommand.Clipboard.Command = ""
	err := setup.AutoCopyUnavailable(noCommand)
	if err == nil || !strings.Contains(err.Error(), "clipboard") {
		t.Errorf("error = %v, should name the missing clipboard command", err)
	}

	// No scope means everywhere: anyone could rewrite the clipboard.
	nowhere := working
	nowhere.Codes.Include = nil
	err = setup.AutoCopyUnavailable(nowhere)
	if err == nil || !strings.Contains(err.Error(), "include") {
		t.Errorf("error = %v, should refuse an unnamed place", err)
	}
}

// The decision carries the switch, scope and shape, so callers need not check the switch.
func TestAutoCopyFromConfig(t *testing.T) {
	t.Parallel()

	got, err := setup.AutoCopy(config.Config{
		Clipboard: config.Clipboard{AutoCopy: true},
		Codes:     config.Codes{Include: []string{"Bridges"}, MinLength: 6, MaxLength: 6},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled {
		t.Error("Enabled should follow clipboard.auto_copy")
	}
	if len(got.Scope.Include) != 1 || got.Scope.Include[0] != "Bridges" {
		t.Errorf("Scope = %+v, want the configured place", got.Scope)
	}
	if got.Rules.MinLength != 6 || got.Rules.MaxLength != 6 {
		t.Errorf("Rules = %+v, want the configured shape", got.Rules)
	}
}
