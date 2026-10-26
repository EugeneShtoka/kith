//go:build windows

package notify

import (
	"strings"
	"testing"
)

// The line reaches cmd.exe as written, inner quotes intact, with stdin as payload.
func TestShellCommandRunsTheLineAsWritten(t *testing.T) {
	t.Parallel()

	cmd := shellCommand(t.Context(), `echo "two words" & more`)
	cmd.Stdin = strings.NewReader("from stdin\r\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `"two words"`) || !strings.Contains(got, "from stdin") {
		t.Errorf("output = %q, want the quoted echo and the stdin payload", got)
	}
}
