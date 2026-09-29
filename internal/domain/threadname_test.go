package domain_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestThreadNameFrom(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, reply, want string }{
		{"plain", "Deploy rollback", "Deploy rollback"},
		{"quoted", `"Deploy rollback"`, "Deploy rollback"},
		{"curly quotes", "“Deploy rollback”", "Deploy rollback"},
		{"trailing period", "Deploy rollback.", "Deploy rollback"},
		{"a preamble is skipped", "Sure! Here is a name:\n\nDeploy rollback", "Deploy rollback"},
		{"a colon in the name itself survives", "Deploy: rollback", "Deploy: rollback"},
		{"a bullet", "- Deploy rollback", "Deploy rollback"},
		{"markdown bold", "**Deploy rollback**", "Deploy rollback"},
		{"collapses space", "Deploy    rollback\n", "Deploy rollback"},
		{"empty", "   \n  ", ""},
		{"too long cuts on a word", "Rolling back the release because the migration locked the table",
			"Rolling back the release because the migration…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := domain.ThreadNameFrom(tc.reply); got != tc.want {
				t.Errorf("ThreadNameFrom(%q) = %q, want %q", tc.reply, got, tc.want)
			}
		})
	}
}

// An answer that is not an attempt at a name is refused rather than cut down to size.
func TestAnAnswerThatIsNotANameIsRefused(t *testing.T) {
	t.Parallel()

	const notAName = "I am ready to respond. Please provide the name of the conversation " +
		"thread, and I will provide a name of the name"
	if got := domain.ThreadNameFrom(notAName); got != "" {
		t.Errorf("ThreadNameFrom(a 114-character non-answer) = %q, want it refused", got)
	}

	// But a real name that runs a little long is still a name, and is trimmed.
	long := "Rolling back the release because the migration locked the table"
	if got := domain.ThreadNameFrom(long); got == "" {
		t.Errorf("ThreadNameFrom(%q) refused a name that is merely long", long)
	}
}
