package setup_test

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// Every word config declares is accepted, and nothing else is.
func TestEveryWordConfigDeclaresIsAccepted(t *testing.T) {
	t.Parallel()

	for _, v := range []struct {
		name    string
		allowed []string
		resolve func(string) (string, error)
		// identity says the resolver returns the word it was given.
		identity bool
	}{
		{"media mode", config.MediaModes(), setup.MediaMode, true},
		{"media detail", config.MediaDetails(), setup.MediaDetail, true},
		{"emoji tier", config.EmojiTiers(), setup.EmojiTier, true},
		{"unread source", config.UnreadSources(), setup.UnreadSource, true},
		{"thread listing", config.ThreadListings(), setup.ThreadListing, true},
		{"skin tone", config.SkinToneNames(), setup.SkinTone, false},
	} {
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()

			if len(v.allowed) == 0 {
				t.Fatal("config declares no words for this setting")
			}
			for _, word := range v.allowed {
				got, err := v.resolve(word)
				if err != nil {
					t.Errorf("%q is in config's vocabulary but the validator refuses it: %v", word, err)
					continue
				}
				if v.identity && got != word {
					t.Errorf("%q resolved to %q", word, got)
				}
				// Case and surrounding space are the user's, not the vocabulary's.
				if _, err := v.resolve("  " + strings.ToUpper(word) + " "); err != nil {
					t.Errorf("%q is refused when written in capitals or padded: %v", word, err)
				}
			}

			// An empty setting means the default, which is the first word.
			got, err := v.resolve("")
			if err != nil {
				t.Fatalf("an unset value is an error: %v", err)
			}
			want, _ := v.resolve(v.allowed[0])
			if got != want {
				t.Errorf("unset resolved to %q, want the first word's value %q", got, want)
			}

			// And a word that is not in the vocabulary is refused, naming the rest.
			_, err = v.resolve("definitely-not-a-real-value")
			if err == nil {
				t.Fatal("an unknown value was accepted")
			}
			for _, word := range v.allowed {
				if !strings.Contains(err.Error(), word) {
					t.Errorf("the error does not offer %q: %v", word, err)
				}
			}
		})
	}
}

// Every skin tone config names has a modifier, and only "none" has none.
func TestEverySkinToneHasAModifier(t *testing.T) {
	t.Parallel()

	for _, name := range config.SkinToneNames() {
		mod, ok := config.SkinToneModifier(name)
		if !ok {
			t.Errorf("%q is in SkinToneNames but SkinToneModifier does not know it", name)
			continue
		}
		if name == config.SkinToneNone && mod != "" {
			t.Errorf("%q carries a modifier %q", name, mod)
		}
		if name != config.SkinToneNone && mod == "" {
			t.Errorf("%q carries no modifier", name)
		}
	}
	if _, ok := config.SkinToneModifier("tan"); ok {
		t.Error("a tone nobody declared was resolved")
	}
}

// Both halves of [display.deleted] are checked, and the error names the key that is
// wrong.
func TestDeletedMessagesRefusesByName(t *testing.T) {
	t.Parallel()

	tests := map[string]config.Deleted{
		"display.deleted.mine":   {Mine: "vanish", Others: "show"},
		"display.deleted.others": {Mine: "show", Others: "vanish"},
	}
	for key, cfg := range tests {
		err := setup.DeletedMessages(cfg)
		if err == nil {
			t.Fatalf("%+v was accepted", cfg)
		}
		if !strings.Contains(err.Error(), key) {
			t.Errorf("the error is %q, which does not name %s", err, key)
		}
	}
	// And what is meant to work, does — the empty config included, which is every
	// config written before this setting existed.
	for _, cfg := range []config.Deleted{{}, {Mine: "hide"}, {Mine: "show", Others: "hide"}} {
		if err := setup.DeletedMessages(cfg); err != nil {
			t.Errorf("DeletedMessages(%+v) = %v", cfg, err)
		}
	}
}
