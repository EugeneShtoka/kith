//go:build signal

package meow

import (
	"strings"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
)

// described is the words a message kith does not show as itself reads as, as on other
// networks: view-once media stays in Signal's apps; a shared contact reads as its
// name, a payment as one. "" for a message shown as itself.
func described(data *signalpb.DataMessage) string {
	switch {
	case data.GetIsViewOnce():
		return "[view-once media: open it in the Signal app]"
	case data.GetBody() != "", len(data.GetAttachments()) > 0, data.GetSticker() != nil, data.GetPollCreate() != nil:
		return ""
	case len(data.GetContact()) > 0:
		var names []string
		for _, c := range data.GetContact() {
			if name := contactName(c); name != "" {
				names = append(names, name)
			}
		}
		return strings.TrimSpace("[contact] " + strings.Join(names, ", "))
	case data.GetPayment() != nil:
		return "[payment]"
	}
	return ""
}

// contactName is a shared contact's name: given and family, else nickname, else
// organization.
func contactName(c *signalpb.DataMessage_Contact) string {
	n := c.GetName()
	if full := strings.TrimSpace(n.GetGivenName() + " " + n.GetFamilyName()); full != "" {
		return full
	}
	if n.GetNickname() != "" {
		return n.GetNickname()
	}
	return c.GetOrganization()
}
