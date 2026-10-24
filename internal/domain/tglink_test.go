package domain_test

import (
	"reflect"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Both spellings (tg: and t.me) of every kind of link.
func TestParseTelegramLinkReadsBothSpellings(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		uri  string
		want domain.TelegramLink
	}{
		"tg://resolve, a username": {
			uri:  "tg://resolve?domain=novosalsa",
			want: domain.TelegramLink{Kind: domain.TelegramLinkUsername, Username: "novosalsa"},
		},
		"tg://resolve, a username and a post": {
			uri:  "tg://resolve?domain=novosalsa&post=35",
			want: domain.TelegramLink{Kind: domain.TelegramLinkUsername, Username: "novosalsa", Post: 35},
		},
		"tg://resolve folds the username to lowercase": {
			uri:  "tg://resolve?domain=NovoSalsa",
			want: domain.TelegramLink{Kind: domain.TelegramLinkUsername, Username: "novosalsa"},
		},
		"tg://privatepost, a channel and a post": {
			uri:  "tg://privatepost?channel=12345&post=78",
			want: domain.TelegramLink{Kind: domain.TelegramLinkPrivateChannel, Channel: 12345, Post: 78},
		},
		"https://t.me, a username": {
			uri:  "https://t.me/novosalsa",
			want: domain.TelegramLink{Kind: domain.TelegramLinkUsername, Username: "novosalsa"},
		},
		"https://t.me, a username and a post": {
			uri:  "https://t.me/novosalsa/35",
			want: domain.TelegramLink{Kind: domain.TelegramLinkUsername, Username: "novosalsa", Post: 35},
		},
		"https://t.me, a private channel": {
			uri:  "https://t.me/c/12345/78",
			want: domain.TelegramLink{Kind: domain.TelegramLinkPrivateChannel, Channel: 12345, Post: 78},
		},
		"https://t.me, a shared preview is the same chat": {
			uri:  "https://t.me/s/novosalsa",
			want: domain.TelegramLink{Kind: domain.TelegramLinkUsername, Username: "novosalsa"},
		},
		"a bare t.me/<name> as people paste it": {
			uri:  "t.me/novosalsa",
			want: domain.TelegramLink{Kind: domain.TelegramLinkUsername, Username: "novosalsa"},
		},
		"https://t.me, an @ prefix is tolerated": {
			uri:  "https://t.me/@novosalsa",
			want: domain.TelegramLink{Kind: domain.TelegramLinkUsername, Username: "novosalsa"},
		},
		"tg://resolve, a message_id spelling is also taken": {
			uri:  "tg://resolve?domain=novosalsa&message_id=35",
			want: domain.TelegramLink{Kind: domain.TelegramLinkUsername, Username: "novosalsa", Post: 35},
		},
		"https://t.me, a topic reply keeps only the post number": {
			uri:  "https://t.me/novosalsa/42/90",
			want: domain.TelegramLink{Kind: domain.TelegramLinkUsername, Username: "novosalsa", Post: 42},
		},
		"https://t.me, a query string after the post is ignored": {
			uri:  "https://t.me/novosalsa/35?single",
			want: domain.TelegramLink{Kind: domain.TelegramLinkUsername, Username: "novosalsa", Post: 35},
		},
		"tg://resolve, a username and a suggested message": {
			uri:  "tg://resolve?domain=novosalsa&text=hi%20there",
			want: domain.TelegramLink{Kind: domain.TelegramLinkUsername, Username: "novosalsa", Text: "hi there"},
		},
		"https://t.me, a username and a suggested message": {
			uri:  "https://t.me/novosalsa?text=hi+there",
			want: domain.TelegramLink{Kind: domain.TelegramLinkUsername, Username: "novosalsa", Text: "hi there"},
		},
		"tg://resolve, a phone number": {
			uri:  "tg://resolve?phone=15551234567&text=hello",
			want: domain.TelegramLink{Kind: domain.TelegramLinkPhone, Phone: "15551234567", Text: "hello"},
		},
		"https://t.me, a phone number": {
			uri:  "https://t.me/+15551234567",
			want: domain.TelegramLink{Kind: domain.TelegramLinkPhone, Phone: "15551234567"},
		},
		"tg://join, an invite": {
			uri:  "tg://join?invite=AbC-d_9xYz",
			want: domain.TelegramLink{Kind: domain.TelegramLinkInvite, Invite: "AbC-d_9xYz"},
		},
		"https://t.me, a +invite": {
			uri:  "https://t.me/+AbC-d_9xYz",
			want: domain.TelegramLink{Kind: domain.TelegramLinkInvite, Invite: "AbC-d_9xYz"},
		},
		"https://t.me, a joinchat invite": {
			uri:  "https://t.me/joinchat/AbC-d_9xYz",
			want: domain.TelegramLink{Kind: domain.TelegramLinkInvite, Invite: "AbC-d_9xYz"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, ok := domain.ParseTelegramLink(tc.uri)
			if !ok {
				t.Fatalf("ParseTelegramLink(%q) said it was not a Telegram link", tc.uri)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseTelegramLink(%q) =\n  %+v\nwant\n  %+v", tc.uri, got, tc.want)
			}
		})
	}
}

// Not Telegram links: anything not understood is refused, so cmd/kith/open does not
// accept it and then nothing can go anywhere with it.
func TestParseTelegramLinkRefusesWhatIsNotOne(t *testing.T) {
	t.Parallel()

	for _, uri := range []string{
		"",
		"https://example.org/",
		"https://matrix.to/#/!abc:example.org",
		"matrix:r/kith:example.org",
		"tg://join?invite=",                   // no hash
		"tg://join?invite=a/b",                // not an invite hash
		"https://t.me/+",                      // neither a number nor a hash
		"https://t.me/+abc/1",                 // an invite names no post
		"https://t.me/joinchat/",              // no hash
		"tg://resolve?phone=+1555",            // the number is digits only
		"tg://resolve?phone=1234567890123456", // longer than E.164
		"tg://addstickers?set=pepe",
		"tg://resolve?domain=",         // no name
		"tg://resolve?domain=1name",    // starts with a digit
		"tg://resolve?domain=has-dash", // telegram does not allow hyphens
		"tg://resolve?domain=has.dot",
		"tg://privatepost?channel=0&post=1",
		"tg://privatepost?channel=-1&post=1",
		"tg://privatepost?post=1", // no channel
		"https://t.me/",
		"https://t.me/c",
		"https://t.me/c/0/1",   // channel 0
		"https://t.me/name/0",  // post 0 is invalid
		"https://t.me/name/-1", // negative
	} {
		if got, ok := domain.ParseTelegramLink(uri); ok {
			t.Errorf("ParseTelegramLink(%q) = %+v, want not a Telegram link", uri, got)
		}
	}
}
