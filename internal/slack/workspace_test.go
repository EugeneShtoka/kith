package slack

import (
	"net/url"
	"testing"
)

// The `d` cookie reaches Slack URL-encoded once, whether pasted encoded (as DevTools
// shows it) or decoded.
func TestCookieValueReachesSlackEncodedOnce(t *testing.T) {
	const browser = "xoxd-DR5W%2Bn3K%2FMIKx%3D%3D"
	for name, in := range map[string]string{
		"copied encoded": browser,
		"copied decoded": "xoxd-DR5W+n3K/MIKx==",
	} {
		if got := url.QueryEscape(cookieValue(in)); got != browser {
			t.Errorf("%s: sent %q, want %q", name, got, browser)
		}
	}
}
