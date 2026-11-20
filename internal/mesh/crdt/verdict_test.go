package crdt_test

import (
	"errors"
	"testing"

	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
)

// A verdict names its own key (a room's or a person's, and a lower-cased tag, either
// holding "/"), or deletes it; a retag names the tag it became. Anything else is
// refused.
func TestAVerdictWriteThatDoesNotReadIsRefused(t *testing.T) {
	t.Parallel()
	a := newReplica(t)
	s := genesis(t, a)
	for name, w := range map[string]crdt.Write{
		"a room's":                {Key: crdt.TagVerdictKey(tgChat, "spam"), Value: []byte(`{"id":"` + tgChat + `","tag":"spam","rule":1,"at":1}`)},
		"a person's":              {Key: crdt.PersonVerdictKey("tg:555", "spam"), Value: []byte(`{"id":"tg:555","tag":"spam","released":true,"at":1}`)},
		"a tag holding a slash":   {Key: crdt.TagVerdictKey(tgChat, "a/b"), Value: []byte(`{"id":"` + tgChat + `","tag":"a/b","at":1}`)},
		"a room's deleted":        {Key: crdt.TagVerdictKey(tgChat, "spam")},
		"a person's deleted":      {Key: crdt.PersonVerdictKey("tg:555", "spam")},
		"a retag":                 {Key: crdt.RetagKey("spam"), Value: []byte(`{"to":"junk"}`)},
		"a tag deleted, retagged": {Key: crdt.RetagKey("spam"), Value: []byte(`{"to":""}`)},
	} {
		if _, err := s.Apply(a.op(s, w)); err != nil {
			t.Errorf("%s: Apply = %v", name, err)
		}
	}
	for name, w := range map[string]crdt.Write{
		"another room":           {Key: crdt.TagVerdictKey(tgChat, "spam"), Value: []byte(`{"id":"tg:1","tag":"spam","at":1}`)},
		"another tag":            {Key: crdt.TagVerdictKey(tgChat, "spam"), Value: []byte(`{"id":"` + tgChat + `","tag":"junk","at":1}`)},
		"a room's as a person's": {Key: crdt.PersonVerdictKey(tgChat, "spam"), Value: []byte(`{"id":"tg:1/x","tag":"spam","at":1}`)},
		"no verdict":             {Key: crdt.TagVerdictKey(tgChat, "spam"), Value: []byte("spam")},
		"nothing deleted":        {Key: "tag/spam"},
		"a retag of none":        {Key: crdt.RetagKey(""), Value: []byte(`{"to":"junk"}`)},
		"a retag to none":        {Key: crdt.RetagKey("spam"), Value: []byte(`{}`)},
		"a retag deleted":        {Key: crdt.RetagKey("spam")},
	} {
		if _, err := s.Apply(a.op(s, w)); !errors.Is(err, crdt.ErrBadWrite) {
			t.Errorf("%s: Apply = %v, want %v", name, err, crdt.ErrBadWrite)
		}
	}
}
