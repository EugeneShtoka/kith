package whatsapp

import (
	"context"
	"regexp"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// numberMentioned is a mention as WhatsApp writes it in the text: "@" and the person's
// LID or number.
var numberMentioned = regexp.MustCompile(`@(\d{6,15})\b`)

// listOldMentions gives the account's cached messages cached before mentions were kept
// the people their text mentions, so they are drawn by name: a number that is a LID
// the store knows is that person, one that is a member's number is that member, and
// any other is left as text (it may be just digits someone typed).
func (a *Adapter) listOldMentions(ctx context.Context, account Account, client *whatsmeow.Client) {
	if a.cache == nil {
		return
	}
	msgs, err := a.cache.UnlistedNumberMentions(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, account.Digits))
	if err != nil {
		a.log.Warn("read the messages with unlisted mentions failed", "account", account.Name, "err", err)
		return
	}
	lookup := a.pnLookup(client)
	members := map[domain.RoomID]map[string]bool{}
	memberOf := func(room domain.RoomID, user string) bool {
		if members[room] == nil {
			members[room] = map[string]bool{}
			list, err := a.cache.Members(ctx, room, 0)
			if err != nil {
				a.log.Warn("read a chat's members failed", "room", room, "err", err)
			}
			for i := range list {
				members[room][list[i].UserID] = true
			}
		}
		return members[room][user]
	}
	listed := 0
	for i := range msgs {
		var mentions []domain.Mention
		for _, match := range numberMentioned.FindAllStringSubmatch(msgs[i].Body, -1) {
			digits := match[1]
			user := ""
			if pn := lookup(ctx, types.NewJID(digits, types.HiddenUserServer)); !pn.IsEmpty() {
				user = domain.NativePerson(domain.ProtocolWhatsApp, pn.ToNonAD().String())
			} else if numbered := domain.NativePerson(domain.ProtocolWhatsApp, types.NewJID(digits, types.DefaultUserServer).String()); memberOf(msgs[i].RoomID, numbered) {
				user = numbered
			}
			if user != "" {
				mentions = append(mentions, domain.Mention{UserID: user, Name: match[0]})
			}
		}
		if len(mentions) == 0 {
			continue
		}
		if err := a.cache.AddMentions(ctx, msgs[i].RoomID, msgs[i].ID, mentions); err != nil {
			a.log.Warn("keep a message's mentions failed", "room", msgs[i].RoomID, "err", err)
			continue
		}
		listed++
	}
	if listed > 0 {
		a.log.Info("listed the mentions of messages cached before they were kept", "account", account.Name, "messages", listed)
	}
}
