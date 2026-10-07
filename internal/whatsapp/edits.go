package whatsapp

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// keepDeletedIf sets [display.deleted] keep: whether a deleted message's words stay in
// the cache. Called at startup, as Matrix's is.
func (a *Adapter) keepDeletedIf(keep bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.keepDeleted = keep
}

// keepsDeleted is [display.deleted] keep.
func (a *Adapter) keepsDeleted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.keepDeleted
}

// onChange handles what changes a message already sent rather than adding one: an
// edit, a deletion, a reaction. false when e is none of them (an ordinary message).
func (a *Adapter) onChange(ctx context.Context, account Account, client *whatsmeow.Client, e *events.Message) bool {
	if r := e.Message.GetReactionMessage(); r != nil {
		a.onReaction(ctx, account, client, e, r)
		return true
	}
	if vote := e.Message.GetPollUpdateMessage(); vote != nil {
		a.onPollVote(ctx, account, client, e, vote)
		return true
	}
	pm := e.Message.GetProtocolMessage()
	if pm == nil {
		return false
	}
	switch pm.GetType() {
	case waE2E.ProtocolMessage_REVOKE:
		a.onDeleted(ctx, account, client, e, pm.GetKey().GetID())
	case waE2E.ProtocolMessage_MESSAGE_EDIT:
		a.onEdited(ctx, account, client, e, pm)
	default:
		// Every other protocol message is the protocol's own business.
	}
	return true
}

// change is where a change lands: the room, the sender, and the message changed.
func (a *Adapter) change(ctx context.Context, account Account, client *whatsmeow.Client, e *events.Message, target string) (domain.RoomID, string, domain.EventID) {
	lookup := a.pnLookup(client)
	room := roomID(account.Digits, chatOf(ctx, &e.Info, lookup))
	from := selfOf(client).pn
	if !e.Info.IsFromMe {
		from = person(ctx, e.Info.Sender, e.Info.SenderAlt, lookup)
	}
	return room, domain.NativePerson(domain.ProtocolWhatsApp, from.String()),
		domain.EventID(domain.NativeID(domain.ProtocolWhatsApp, account.Digits, target))
}

// onEdited folds an edit onto the message it replaces, as a Matrix edit is folded.
func (a *Adapter) onEdited(ctx context.Context, account Account, client *whatsmeow.Client, e *events.Message, pm *waE2E.ProtocolMessage) {
	body, _, ok := content(pm.GetEditedMessage())
	if !ok || body == "" {
		return
	}
	room, sender, target := a.change(ctx, account, client, e, pm.GetKey().GetID())
	words, format := formatted(body)
	edit := domain.Message{
		ID: target, RoomID: room, Sender: sender, Body: words, Format: format, Edited: true,
		RevisionID: domain.EventID(domain.NativeID(domain.ProtocolWhatsApp, account.Digits, e.Info.ID)),
		Timestamp:  e.Info.Timestamp, EditedAt: e.Info.Timestamp,
	}
	if a.cache != nil {
		a.record(ctx, account, edit, func() {})
	}
	emit(a, a.messages, edit)
}

// onDeleted marks a message deleted ("delete for everyone"), keeping its words only
// under [display.deleted] keep, and tells the clients.
func (a *Adapter) onDeleted(ctx context.Context, account Account, client *whatsmeow.Client, e *events.Message, target string) {
	room, sender, id := a.change(ctx, account, client, e, target)
	keep := a.keepsDeleted()
	gone := domain.Message{ID: id, RoomID: room, Redacted: true, RedactedBy: sender, RedactedAt: e.Info.Timestamp}
	if a.cache != nil {
		if err := a.cache.MarkRedacted(ctx, room, id, sender, "", e.Info.Timestamp, keep); err != nil {
			a.log.Warn("mark a message deleted failed", "room", room, "err", err)
		}
		if keep {
			if kept, ok, err := a.cache.MessageByID(ctx, room, id); err == nil && ok {
				gone.Body, gone.Format = kept.Body, kept.Format
			}
		}
		if a.onChanged != nil {
			a.onChanged(room)
		}
		a.recount(ctx, room)
	}
	emit(a, a.messages, gone)
}

// reactionID is the one reaction a person has on a message: WhatsApp keeps one each,
// a new one replacing it and an empty one taking it back.
func reactionID(account string, target string, sender types.JID) domain.EventID {
	return domain.EventID(domain.NativeID(domain.ProtocolWhatsApp, account, "reaction:"+target+":"+sender.User))
}

// onReaction records a reaction, replacing the sender's earlier one on that message,
// or takes it back.
func (a *Adapter) onReaction(ctx context.Context, account Account, client *whatsmeow.Client, e *events.Message, r *waE2E.ReactionMessage) {
	target := r.GetKey().GetID()
	room, sender, targetID := a.change(ctx, account, client, e, target)
	senderJID, _ := types.ParseJID(domain.ParseID(sender).Native)
	reaction := domain.Reaction{
		ID: reactionID(account.Digits, target, senderJID), RoomID: room, Target: targetID, Sender: sender, Key: r.GetText(),
	}
	a.applyReaction(ctx, reaction)
}

// applyReaction replaces whatever reaction has reaction's ID with it (none, for an
// empty key), streaming the change as a removal and an add.
func (a *Adapter) applyReaction(ctx context.Context, reaction domain.Reaction) {
	if a.cache != nil {
		old, had, err := a.cache.DeleteReaction(ctx, reaction.ID)
		if err != nil {
			a.log.Warn("drop an earlier reaction failed", "room", reaction.RoomID, "err", err)
		}
		if had {
			emit(a, a.reactions, domain.ReactionUpdate{Reaction: old, Removed: true})
		}
		if reaction.Key == "" {
			return
		}
		if err := a.cache.SaveReactions(ctx, []domain.Reaction{reaction}); err != nil {
			a.log.Warn("cache a reaction failed", "room", reaction.RoomID, "err", err)
		}
	}
	if reaction.Key != "" {
		emit(a, a.reactions, domain.ReactionUpdate{Reaction: reaction})
	}
}

// target is what an outgoing change is about: the chat, the message, who wrote it,
// and the account's client.
func (a *Adapter) target(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (types.JID, types.MessageID, types.JID, Account, *whatsmeow.Client, error) {
	room, event := domain.ParseID(string(roomID)), domain.ParseID(string(eventID))
	chat, err := types.ParseJID(room.Native)
	if err != nil {
		return chat, "", types.EmptyJID, Account{}, nil, fmt.Errorf("whatsapp: %s is not a chat: %w", roomID, err)
	}
	account, client, ok := a.clientFor(room.Account)
	if !ok {
		return chat, "", types.EmptyJID, Account{}, nil, fmt.Errorf("whatsapp: account %s is not connected: %w", room.Account, errNetworkOff)
	}
	sender := selfOf(client).pn
	if a.cache != nil {
		if from, err := a.cache.SenderOf(ctx, roomID, eventID); err == nil && from != "" {
			if jid, perr := types.ParseJID(domain.ParseID(from).Native); perr == nil {
				sender = jid
			}
		}
	}
	return chat, event.Native, sender, account, client, nil
}

// Redact deletes a message for everyone. WhatsApp lets you delete only your own (and
// in a group you administer, others'); its refusal comes back as the error.
func (a *Adapter) Redact(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, _ string) error {
	chat, id, sender, account, client, err := a.target(ctx, roomID, eventID)
	if err != nil {
		return err
	}
	if _, err := client.SendMessage(ctx, chat, client.BuildRevoke(chat, sender, id)); err != nil {
		return fmt.Errorf("whatsapp: delete in %s: %w", roomID, err)
	}
	// WhatsApp does not echo it: delete here as the echo would have.
	a.onDeleted(ctx, account, client, &events.Message{Info: types.MessageInfo{
		MessageSource: types.MessageSource{Chat: chat, Sender: selfOf(client).pn, IsFromMe: true, IsGroup: chat.Server == types.GroupServer},
		Timestamp:     time.Now(),
	}}, id)
	return nil
}

// SendReaction reacts to a message, replacing our earlier reaction on it (WhatsApp
// keeps one per person).
func (a *Adapter) SendReaction(ctx context.Context, roomID domain.RoomID, target domain.EventID, key string) error {
	if isChannel(roomID) {
		return errChannelReaction
	}
	chat, id, sender, account, client, err := a.target(ctx, roomID, target)
	if err != nil {
		return err
	}
	if _, err := client.SendMessage(ctx, chat, client.BuildReaction(chat, sender, id, key)); err != nil {
		return fmt.Errorf("whatsapp: react in %s: %w", roomID, err)
	}
	own := selfOf(client).pn
	a.applyReaction(ctx, domain.Reaction{
		ID: reactionID(account.Digits, id, own), RoomID: roomID, Target: target,
		Sender: domain.NativePerson(domain.ProtocolWhatsApp, own.String()), Key: key,
	})
	return nil
}

// edit sends draft as a new version of the message it edits, and folds it in here
// (WhatsApp does not echo it).
func (a *Adapter) edit(ctx context.Context, roomID domain.RoomID, draft domain.Draft) error {
	chat, id, _, account, client, err := a.target(ctx, roomID, draft.Edits)
	if err != nil {
		return err
	}
	text, mentioned := composed(draft)
	content, _ := a.outgoing(ctx, account.Digits, roomID, text, mentioned, "")
	resp, err := client.SendMessage(ctx, chat, client.BuildEdit(chat, id, content))
	if err != nil {
		return fmt.Errorf("whatsapp: edit in %s: %w", roomID, err)
	}
	own := selfOf(client).pn
	edit := domain.Message{
		ID: draft.Edits, RoomID: roomID, Sender: domain.NativePerson(domain.ProtocolWhatsApp, own.String()),
		Edited: true, Timestamp: resp.Timestamp, EditedAt: resp.Timestamp,
		RevisionID: domain.EventID(domain.NativeID(domain.ProtocolWhatsApp, account.Digits, resp.ID)),
	}
	edit.Body, edit.Format = formatted(text)
	if a.cache != nil {
		a.record(ctx, account, edit, func() {})
	}
	emit(a, a.messages, edit)
	return nil
}
