package whatsapp

import (
	"context"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A member is addressed by directory number or WhatsApp ID; other networks' IDs fail.
func TestAMembersAddressIsTheirNumber(t *testing.T) {
	t.Parallel()
	for person, want := range map[string]string{
		domain.PhoneID("15550100001"): "15550100001@s.whatsapp.net",
		domain.NativePerson(domain.ProtocolWhatsApp, "15550100002@s.whatsapp.net"): "15550100002@s.whatsapp.net",
	} {
		if jid, ok := personJID(person); !ok || jid.String() != want {
			t.Errorf("personJID(%q) = (%v, %v), want %s", person, jid, ok, want)
		}
	}
	if _, ok := personJID("telegram:7"); ok {
		t.Error("a Telegram person has a WhatsApp address")
	}
}

// A too-long group name is refused locally.
func TestAGroupNameIsAtMostWhatWhatsAppTakes(t *testing.T) {
	t.Parallel()
	a := &Adapter{}
	_, err := a.CreateRoom(context.Background(), domain.NewRoom{
		Name: strings.Repeat("я", groupNameMost+1), On: domain.AccountRooms(domain.ProtocolWhatsApp, "1"), Kind: domain.ChatGroup,
	})
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("err = %v, want the name refused", err)
	}
}
