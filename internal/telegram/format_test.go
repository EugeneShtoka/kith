package telegram

import (
	"testing"

	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Entities map to nested markup at UTF-16 offsets (an emoji counts two); URLs link to
// themselves; name entities become mentions; out-of-range or unsupported entities
// leave text as is.
func TestEntitiesAreMarkup(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		text     string
		entities []tg.MessageEntityClass
		markup   string
		body     string
	}{
		"bold":           {"hi there", []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 3, Length: 5}}, "hi <b>there</b>", "hi there"},
		"after an emoji": {"😀 bold", []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 3, Length: 4}}, "😀 <b>bold</b>", "😀 bold"},
		"overlapping": {"abcdef", []tg.MessageEntityClass{
			&tg.MessageEntityBold{Offset: 0, Length: 4}, &tg.MessageEntityItalic{Offset: 2, Length: 4},
		}, "<b>ab<i>cd</i></b><i>ef</i>", "abcdef"},
		"nested": {"abcdef", []tg.MessageEntityClass{
			&tg.MessageEntityItalic{Offset: 1, Length: 2}, &tg.MessageEntityBold{Offset: 0, Length: 6},
		}, "<b>a<i>bc</i>def</b>", "abcdef"},
		"link": {"see docs", []tg.MessageEntityClass{&tg.MessageEntityTextURL{Offset: 4, Length: 4, URL: "https://example.org/a?b=1&c=2"}},
			`see <a href="https://example.org/a?b=1&amp;c=2">docs</a>`, "see docs"},
		"url": {"at https://example.org", []tg.MessageEntityClass{&tg.MessageEntityURL{Offset: 3, Length: 19}},
			`at <a href="https://example.org">https://example.org</a>`, "at https://example.org"},
		"spoiler": {"it was him", []tg.MessageEntityClass{&tg.MessageEntitySpoiler{Offset: 7, Length: 3}},
			`it was <span data-mx-spoiler>him</span>`, "it was him"},
		"past the end": {"short", []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 2, Length: 10}}, "", "short"},
		"a hashtag":    {"#tag", []tg.MessageEntityClass{&tg.MessageEntityHashtag{Offset: 0, Length: 4}}, "", "#tag"},
	} {
		body, f, _ := formatted(c.text, c.entities)
		if f.Markup() != c.markup || body != c.body {
			t.Errorf("%s: (%q, %q), want (%q, %q)", name, body, f.Markup(), c.body, c.markup)
		}
	}
	_, _, mentions := formatted("hi Dana!", []tg.MessageEntityClass{&tg.MessageEntityMentionName{Offset: 3, Length: 4, UserID: 7}})
	if len(mentions) != 1 || mentions[0] != (domain.Mention{UserID: "telegram:7", Name: "Dana"}) {
		t.Errorf("mentions = %+v", mentions)
	}
}
