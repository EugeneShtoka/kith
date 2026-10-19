//go:build signal

package meow

import (
	"testing"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/backuppb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
)

// What kith does not show as itself reads as on other networks: view-once media (even
// with words) as staying in Signal's apps, a shared contact by its name, a payment as
// one; a message shown as itself, or a profile key, reads as nothing more.
func TestWhatKithDoesNotShowReadsAsItIs(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		data *signalpb.DataMessage
		want string
	}{
		{"view-once", &signalpb.DataMessage{IsViewOnce: new(true), Attachments: []*signalpb.AttachmentPointer{{}}}, "[view-once media: open it in the Signal app]"},
		{"contacts", &signalpb.DataMessage{Contact: []*signalpb.DataMessage_Contact{
			{Name: &signalpb.DataMessage_Contact_Name{GivenName: new("Noa"), FamilyName: new("Bar")}},
			{Name: &signalpb.DataMessage_Contact_Name{Nickname: new("Zed")}},
			{Organization: new("Acme")},
		}}, "[contact] Noa Bar, Zed, Acme"},
		{"nameless contact", &signalpb.DataMessage{Contact: []*signalpb.DataMessage_Contact{{}}}, "[contact]"},
		{"payment", &signalpb.DataMessage{Payment: &signalpb.DataMessage_Payment{}}, "[payment]"},
		{"text", &signalpb.DataMessage{Body: new("hi"), Payment: &signalpb.DataMessage_Payment{}}, ""},
		{"profile key", &signalpb.DataMessage{ProfileKey: []byte{1}}, ""},
	} {
		if got := described(c.data); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// The phone's history's shared contacts, view-once messages and payments read as live
// ones do, keeping their reactions; anything else is none of them.
func TestArchivedOthersReadAsLiveOnes(t *testing.T) {
	t.Parallel()
	liked := []*backuppb.Reaction{{Emoji: "👍"}}
	for _, c := range []struct {
		name  string
		item  *backuppb.ChatItem
		want  string
		liked bool
	}{
		{"contact", &backuppb.ChatItem{Item: &backuppb.ChatItem_ContactMessage{ContactMessage: &backuppb.ContactMessage{
			Contact: &backuppb.ContactAttachment{Name: &backuppb.ContactAttachment_Name{GivenName: "Noa"}}, Reactions: liked,
		}}}, "[contact] Noa", true},
		{"view-once", &backuppb.ChatItem{Item: &backuppb.ChatItem_ViewOnceMessage{ViewOnceMessage: &backuppb.ViewOnceMessage{Reactions: liked}}}, "[view-once media: open it in the Signal app]", true},
		{"payment", &backuppb.ChatItem{Item: &backuppb.ChatItem_PaymentNotification{PaymentNotification: &backuppb.PaymentNotification{}}}, "[payment]", false},
	} {
		data := &signalpb.DataMessage{}
		reactions, ok := archivedOther(c.item, data)
		if !ok || described(data) != c.want || (len(reactions) == 1) != c.liked {
			t.Errorf("%s: %v %q %v, want %q", c.name, ok, described(data), reactions, c.want)
		}
	}
	if _, ok := archivedOther(&backuppb.ChatItem{Item: &backuppb.ChatItem_StandardMessage{}}, &signalpb.DataMessage{}); ok {
		t.Error("a standard message was taken for another kind")
	}
}
