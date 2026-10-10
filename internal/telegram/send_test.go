package telegram

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgtest"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A draft's Markdown is Telegram's entities, at UTF-16 offsets (an emoji before them
// counts twice); a plain draft, or one with no formatting, goes as typed; a person it
// names is a mention when their access hash is known, the longer name claiming its
// words first.
func TestDraftsAreSentWithEntities(t *testing.T) {
	t.Parallel()
	known := func(user int64) (int64, bool) { return user * 10, user != 9 }
	for name, c := range map[string]struct {
		draft    domain.Draft
		text     string
		entities []tg.MessageEntityClass
	}{
		"bold":           {domain.Draft{Body: "**hi** there"}, "hi there", []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 2}}},
		"after an emoji": {domain.Draft{Body: "😀 *it*"}, "😀 it", []tg.MessageEntityClass{&tg.MessageEntityItalic{Offset: 3, Length: 2}}},
		"link":           {domain.Draft{Body: "[docs](https://example.org)"}, "docs", []tg.MessageEntityClass{&tg.MessageEntityTextURL{Offset: 0, Length: 4, URL: "https://example.org"}}},
		"code":           {domain.Draft{Body: "run `ls`"}, "run ls", []tg.MessageEntityClass{&tg.MessageEntityCode{Offset: 4, Length: 2}}},
		"spoiler":        {domain.Draft{Body: "it was ||him||"}, "it was him", []tg.MessageEntityClass{&tg.MessageEntitySpoiler{Offset: 7, Length: 3}}},
		"plain":          {domain.Draft{Body: "**as typed**", Plain: true}, "**as typed**", nil},
		"no formatting":  {domain.Draft{Body: "a < b & c"}, "a < b & c", nil},
		"mentions": {domain.Draft{Body: "Daniel and Dan, not Eve", Mentions: []domain.Mention{
			{UserID: "telegram:7", Name: "Dan"}, {UserID: "telegram:8", Name: "Daniel"}, {UserID: "telegram:9", Name: "Eve"},
		}}, "Daniel and Dan, not Eve", []tg.MessageEntityClass{
			&tg.InputMessageEntityMentionName{Offset: 0, Length: 6, UserID: &tg.InputUser{UserID: 8, AccessHash: 80}},
			&tg.InputMessageEntityMentionName{Offset: 11, Length: 3, UserID: &tg.InputUser{UserID: 7, AccessHash: 70}},
		}},
	} {
		text, entities, _ := outgoing(c.draft, known)
		if text != c.text || !reflect.DeepEqual(entities, c.entities) {
			t.Errorf("%s: (%q, %#v), want (%q, %#v)", name, text, entities, c.text, c.entities)
		}
	}
}

// A message sent goes to the chat as Telegram's request — its entities, the message
// it replies to, a random ID the same for the same draft sent again — and is cached and
// handed to the clients under the ID Telegram gave it.
func TestASentMessageIsCachedUnderItsID(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.serveUpdates(&updatesOf{pts: 1})
	var mu sync.Mutex
	var sent []tg.MessagesSendMessageRequest
	f.cluster.Dispatch(2, "dc2").HandleFunc(tg.MessagesSendMessageRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.MessagesSendMessageRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		mu.Lock()
		sent = append(sent, req)
		mu.Unlock()
		return sendResult(s, r, &tg.UpdateShortSentMessage{ID: 77, Date: int(time.Now().Unix()), Pts: 2, PtsCount: 1})
	})
	st := openStore(t)
	knowDana(t, st)
	a, cache := loggedInWithStore(t, f, st)
	room := domain.RoomID("telegram:42/7")
	draft := domain.Draft{Body: "**yes**", ReplyTo: "telegram:42/7/5", TxnID: "t1"}
	if err := a.Send(t.Context(), room, draft); err != nil {
		t.Fatal(err)
	}
	if err := a.Send(t.Context(), room, draft); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	first, again := sent[0], sent[1]
	mu.Unlock()
	reply, _ := first.ReplyTo.(*tg.InputReplyToMessage)
	if first.Message != "yes" || len(first.Entities) != 1 || reply == nil || reply.ReplyToMsgID != 5 || first.RandomID != again.RandomID {
		t.Errorf("sent %+v (reply %+v), again with random ID %d", first, reply, again.RandomID)
	}
	if p, ok := first.Peer.(*tg.InputPeerUser); !ok || p.UserID != 7 || p.AccessHash != dana.AccessHash {
		t.Errorf("sent to %+v, want Dana by her access hash", first.Peer)
	}
	msgs, _ := cache.Messages(t.Context(), room, 10)
	if len(msgs) != 1 || msgs[0].ID != "telegram:42/7/77" || msgs[0].Sender != "telegram:42" || msgs[0].ReplyTo != "telegram:42/7/5" {
		t.Errorf("cached %+v", msgs)
	}
	select {
	case msg := <-a.Messages():
		if msg.ID != "telegram:42/7/77" {
			t.Errorf("heard %s", msg.ID)
		}
	case <-time.After(5 * time.Second):
		t.Error("the sent message was not heard")
	}
}
