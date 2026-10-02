package whatsapp

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

var me = self{pn: pn(ownDigits), lid: lid("100000000000009")}

func noLookup(context.Context, types.JID) types.JID { return types.EmptyJID }

// A person is kept under their phone number whenever WhatsApp or the store has it.
func TestAPersonIsTheirPhoneNumberWhenKnown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	known := func(_ context.Context, l types.JID) types.JID {
		if l == lid(samLID) {
			return pn("972500000004")
		}
		return types.EmptyJID
	}
	for _, c := range []struct {
		jid, alt, want types.JID
	}{
		{pn(danaPhone), types.EmptyJID, pn(danaPhone)},
		{lid(danaLID), pn(danaPhone), pn(danaPhone)},
		{lid(samLID), types.EmptyJID, pn("972500000004")},
		{lid("100000000000099"), types.EmptyJID, lid("100000000000099")},
	} {
		if got := person(ctx, c.jid, c.alt, known); got != c.want {
			t.Errorf("person(%s, %s) = %s, want %s", c.jid, c.alt, got, c.want)
		}
	}
}

// A direct chat is one room whichever way WhatsApp addressed a message in it; a group
// is its own.
func TestAChatIsOneRoomHoweverAddressed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, c := range map[string]struct {
		info types.MessageInfo
		want types.JID
	}{
		"theirs, by LID":     {types.MessageInfo{MessageSource: types.MessageSource{Chat: lid(danaLID), Sender: lid(danaLID), SenderAlt: pn(danaPhone)}}, pn(danaPhone)},
		"theirs, by number":  {types.MessageInfo{MessageSource: types.MessageSource{Chat: pn(danaPhone), Sender: pn(danaPhone)}}, pn(danaPhone)},
		"mine, from a phone": {types.MessageInfo{MessageSource: types.MessageSource{Chat: lid(danaLID), Sender: me.pn, IsFromMe: true, RecipientAlt: pn(danaPhone)}}, pn(danaPhone)},
		"a group":            {types.MessageInfo{MessageSource: types.MessageSource{Chat: types.NewJID("1203", types.GroupServer), Sender: lid(danaLID), IsGroup: true}}, types.NewJID("1203", types.GroupServer)},
	} {
		if got := chatOf(ctx, &c.info, noLookup); got != c.want {
			t.Errorf("%s: chat = %s, want %s", name, got, c.want)
		}
	}
}

// What a message says, what it answers and whom it names; you, when it names you.
func TestAMessageAsKithKeepsIt(t *testing.T) {
	t.Parallel()
	info := &types.MessageInfo{ID: "3EB0AA", Timestamp: time.Unix(1700000000, 0)}
	room := waRoom
	msg := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: new("@" + ownDigits + " see above"),
		ContextInfo: &waE2E.ContextInfo{
			StanzaID:     new("3EB0PREV"),
			MentionedJID: []string{me.pn.String(), pn(danaPhone).String()},
		},
	}}
	got, ok := incoming(ownDigits, room, info, msg, "whatsapp:"+danaPhone+"@s.whatsapp.net", me)
	if !ok {
		t.Fatal("a text message was dropped")
	}
	if got.ID != domain.EventID("whatsapp:"+ownDigits+"/3EB0AA") || got.RoomID != room || !got.Timestamp.Equal(info.Timestamp) {
		t.Errorf("message = %+v", got)
	}
	if got.ReplyTo != domain.EventID("whatsapp:"+ownDigits+"/3EB0PREV") {
		t.Errorf("ReplyTo = %q", got.ReplyTo)
	}
	if !got.Mentioned || len(got.Mentions) != 2 || got.Mentions[1].UserID != "whatsapp:"+danaPhone+"@s.whatsapp.net" {
		t.Errorf("mentions = %+v, mentioned = %v", got.Mentions, got.Mentioned)
	}
	for want, m := range map[string]*waE2E.Message{
		"hi":                   {Conversation: new("hi")},
		"[photo] look":         {ImageMessage: &waE2E.ImageMessage{Caption: new("look")}},
		"[voice message]":      {AudioMessage: &waE2E.AudioMessage{PTT: new(true)}},
		"[document: plan.pdf]": {DocumentMessage: &waE2E.DocumentMessage{FileName: new("plan.pdf")}},
		"[poll] Lunch?":        {PollCreationMessage: &waE2E.PollCreationMessage{Name: new("Lunch?")}},
	} {
		if got, ok := incoming(ownDigits, room, info, m, "x", me); !ok || got.Body != want {
			t.Errorf("body = (%q, %v), want %q", got.Body, ok, want)
		}
	}
	for name, m := range map[string]*waE2E.Message{
		"a reaction": {ReactionMessage: &waE2E.ReactionMessage{Text: new("👍")}},
		"a protocol": {ProtocolMessage: &waE2E.ProtocolMessage{}},
		"nothing":    nil,
	} {
		if _, ok := incoming(ownDigits, room, info, m, "x", me); ok {
			t.Errorf("%s was kept as a message", name)
		}
	}
}
