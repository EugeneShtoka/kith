package setup

import "testing"

// The two halves of a session are told apart before Slack is asked.
func TestASlackSessionHasBothHalves(t *testing.T) {
	t.Parallel()
	if err := CheckSlackSession("xoxc-1", "xoxd-2"); err != nil {
		t.Errorf("a whole session: %v", err)
	}
	for _, swapped := range [][2]string{{"xoxd-2", "xoxc-1"}, {"", "xoxd-2"}, {"xoxc-1", "d=xoxd-2"}} {
		if CheckSlackSession(swapped[0], swapped[1]) == nil {
			t.Errorf("%q / %q accepted", swapped[0], swapped[1])
		}
	}
}

// A suggested name is the number's country, or the workspace's address, kept apart
// from the names taken.
func TestSuggestedNames(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ got, want string }{
		{WhatsAppName("447700900123", nil), "gb"},
		{WhatsAppName("12025550147", []string{"gb"}), "us"},
		{WhatsAppName("4915200000000", nil), "de"},
		{WhatsAppName("79161234567", nil), "ru"},
		{WhatsAppName("447700900000", []string{"gb", "gb2"}), "gb3"},
		{WhatsAppName("8881234567", nil), "wa4567"}, // +888 is no country's
		{SlackName("acme", nil), "acme"},
		{SlackName("T0123456789", []string{"work"}), "work2"},
	} {
		if c.got != c.want {
			t.Errorf("suggested %q, want %q", c.got, c.want)
		}
	}
}

// Every calling code is prefix-free against the others, so the longest match is the
// only one: no code is the start of another.
func TestCallingCodesArePrefixFree(t *testing.T) {
	t.Parallel()
	for code := range callingCodes {
		for other := range callingCodes {
			if code != other && len(other) > len(code) && other[:len(code)] == code {
				t.Errorf("+%s is the start of +%s", code, other)
			}
		}
	}
}
