package slack

import (
	"net/url"
	"testing"
)

// The `d` cookie reaches Slack as the browser holds it — URL-encoded once — whether it
// was copied encoded (as DevTools shows it) or decoded; slackgo.OptionCookie encodes
// what cookieValue returns.
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
