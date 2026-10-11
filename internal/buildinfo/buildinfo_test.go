package buildinfo

import (
	"strings"
	"testing"
)

func TestStringWithInjectedValues(t *testing.T) {
	origV, origC, origD := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = origV, origC, origD })

	Version, Commit, Date = "v1.2.3", "abc1234", "2026-08-16T00:00:00Z"

	got := String("kith")
	for _, want := range []string{"kith v1.2.3", "commit: abc1234", "built:  2026-08-16T00:00:00Z"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, want it to contain %q", got, want)
		}
	}
}

func TestStringOmitsEmptyFields(t *testing.T) {
	origV, origC, origD := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = origV, origC, origD })

	// No commit/date injected and no VCS info under go test: those lines are omitted.
	Version, Commit, Date = "v9.9.9", "", ""

	got := String("kith")
	if !strings.HasPrefix(got, "kith v9.9.9") {
		t.Fatalf("String() = %q, want prefix %q", got, "kith v9.9.9")
	}
	if strings.Contains(got, "commit: \n") || strings.HasSuffix(got, "commit: ") {
		t.Errorf("String() rendered an empty commit line: %q", got)
	}
}

func TestVersionFallsBackToDev(t *testing.T) {
	origV := Version
	t.Cleanup(func() { Version = origV })

	Version = ""
	if v := version(nil); v != "dev" {
		t.Fatalf("version(nil) = %q, want dev", v)
	}
}
