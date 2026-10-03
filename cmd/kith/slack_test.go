package main

import "testing"

// The two halves of a session are told apart before Slack is asked.
func TestASlackSessionHasBothHalves(t *testing.T) {
	t.Parallel()
	if err := checkSlackSession("xoxc-1", "xoxd-2"); err != nil {
		t.Errorf("a whole session: %v", err)
	}
	for _, swapped := range [][2]string{{"xoxd-2", "xoxc-1"}, {"", "xoxd-2"}, {"xoxc-1", "d=xoxd-2"}} {
		if checkSlackSession(swapped[0], swapped[1]) == nil {
			t.Errorf("%q / %q accepted", swapped[0], swapped[1])
		}
	}
}
