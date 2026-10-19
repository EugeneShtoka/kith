//go:build signal

package meow

import (
	"context"
	"encoding/base64"
	"slices"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/events"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/backuppb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/store"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
)

// When this device is linked, the phone may transfer the account's history: an
// archive of its chats, uploaded once. The helper fetches it on the first connection
// after linking (signalmeow keeps what it holds in its store), then hands kithd each
// chat as History, a page at a time. A chat leaves the store only once kithd has kept
// all of it, so a sync cut short (kithd gone, the helper ended) resumes at the next
// connection; kithd keeps a page again as the same messages.

// historyPage is how many messages one History carries.
const historyPage = 500

// syncHistory fetches the phone's transfer when one is owed, then hands kithd every
// chat the store still holds.
func (a *account) syncHistory(ctx context.Context) {
	if a.cli.Store.EphemeralBackupKey != nil {
		a.log.Info("waiting for the phone's history transfer")
		meta, err := a.cli.WaitForTransfer(ctx)
		switch {
		case err != nil:
			a.log.Warn("the history transfer did not come; asked again at the next connection", "err", err)
			return
		case meta.Error != "":
			// Declined on the phone: none is coming, so stop asking.
			a.log.Info("the phone sent no history", "why", meta.Error)
			a.cli.Store.EphemeralBackupKey = nil
			if err := a.cli.Store.DeviceStore.PutDevice(ctx, &a.cli.Store.DeviceData); err != nil {
				a.log.Warn("forgetting the history transfer failed", "err", err)
			}
		default:
			if err := a.cli.FetchAndProcessTransfer(ctx, meta); err != nil {
				a.log.Warn("the history transfer could not be read", "err", err)
				return
			}
			a.log.Info("the phone's history transfer came")
		}
	}
	chats, err := a.cli.Store.BackupStore.GetBackupChats(ctx)
	if err != nil {
		a.log.Warn("the history's chats could not be read", "err", err)
		return
	}
	if len(chats) == 0 {
		return
	}
	a.log.Info("handing kithd the history", "chats", len(chats))
	h := &history{a: a, recipients: map[uint64]*backuppb.Recipient{}}
	for _, chat := range chats {
		if ctx.Err() != nil {
			return
		}
		if h.chat(ctx, chat) {
			if err := a.cli.Store.BackupStore.DeleteBackupChat(ctx, chat.Id); err != nil {
				a.log.Warn("a synced chat could not be let go", "err", err)
			}
		}
	}
	a.log.Info("kithd has the history")
}

// history reads one account's archive.
type history struct {
	a          *account
	recipients map[uint64]*backuppb.Recipient
}

// recipient is an archive recipient by its ID there, nil when it has none.
func (h *history) recipient(ctx context.Context, id uint64) *backuppb.Recipient {
	if r, ok := h.recipients[id]; ok {
		return r
	}
	r, err := h.a.cli.Store.BackupStore.GetBackupRecipient(ctx, id)
	if err != nil {
		r = nil
	}
	h.recipients[id] = r
	return r
}

// aciOf is a recipient's ACI, uuid.Nil for one with none (a group, a phone number only).
func (h *history) aciOf(ctx context.Context, id uint64) uuid.UUID {
	switch d := h.recipient(ctx, id).GetDestination().(type) {
	case *backuppb.Recipient_Self:
		return h.a.cli.Store.ACI
	case *backuppb.Recipient_Contact:
		if aci := d.Contact.GetAci(); len(aci) == 16 {
			return uuid.UUID(aci)
		}
	}
	return uuid.Nil
}

// chat hands kithd one chat's history, newest page first, and reports whether all of
// it was kept (or there was nothing of it to keep).
func (h *history) chat(ctx context.Context, chat *store.BackupChat) bool {
	id, group, ok := h.chatID(ctx, chat)
	if !ok {
		return true
	}
	var anchor time.Time
	for {
		items, err := h.a.cli.Store.BackupStore.GetBackupChatItems(ctx, chat.Id, anchor, false, historyPage)
		if err != nil {
			h.a.cli.Log.Warn().Err(err).Msg("a chat's history could not be read")
			return false
		}
		page := &signalv1.History{
			Chat: id, Group: group,
			Timer: uint32(chat.GetExpirationTimerMs() / 1000), TimerVersion: chat.GetExpireTimerVersion(), //nolint:gosec // Signal's timers fit
		}
		for _, item := range slices.Backward(items) { // oldest first
			if m, read := h.message(ctx, id, group, item); m != nil {
				page.Messages = append(page.Messages, m)
				if read {
					page.ReadThrough = max(page.GetReadThrough(), m.GetSent())
				}
			}
		}
		h.a.deliver(&signalv1.EventsResponse{Event: &signalv1.EventsResponse_History{History: page}})
		if len(items) < historyPage {
			return true
		}
		// Messages < anchor come next: the oldest's time again, plus one, so another sent
		// in the same millisecond is not skipped (the oldest comes twice, kept once).
		anchor = time.UnixMilli(int64(items[len(items)-1].GetDateSent()) + 1) //nolint:gosec // a Signal timestamp fits
		if len(items) == historyPage && items[0].GetDateSent() == items[len(items)-1].GetDateSent() {
			return true // a page all of one millisecond: nothing older can be reached
		}
	}
}

// chatID is an archive chat as kith names chats: the other side's ACI, the account's own
// (Note to Self), or the group's ID. ok is false for one kith does not keep: a one-to-one
// chat with no messages, or a recipient of another kind.
func (h *history) chatID(ctx context.Context, chat *store.BackupChat) (string, bool, bool) {
	switch d := h.recipient(ctx, chat.RecipientId).GetDestination().(type) {
	case *backuppb.Recipient_Self:
		return h.a.aci, false, true
	case *backuppb.Recipient_Contact:
		aci := d.Contact.GetAci()
		if len(aci) != 16 || chat.TotalMessages == 0 {
			return "", false, false
		}
		return uuid.UUID(aci).String(), false, true
	case *backuppb.Recipient_Group:
		if len(d.Group.GetMasterKey()) != libsignalgo.GroupMasterKeyLength {
			return "", false, false
		}
		gid, err := h.a.cli.StoreMasterKey(ctx, types.SerializedGroupMasterKey(base64.StdEncoding.EncodeToString(d.Group.GetMasterKey())))
		if err != nil {
			h.a.cli.Log.Warn().Err(err).Msg("an archived group's key could not be kept")
			return "", false, false
		}
		h.a.describe(ctx, string(gid), d.Group.GetSnapshot().GetVersion())
		return string(gid), true, true
	}
	return "", false, false
}

// message is an archived item as a wire message, with its reactions, and whether the
// account had read it; nil for one kith does not show: what is no plain message or
// sticker (a deleted one, a payment, a view-once one), or one with no author it knows.
func (h *history) message(ctx context.Context, chat string, group bool, item *backuppb.ChatItem) (*signalv1.Message, bool) {
	sm, st, poll := item.GetStandardMessage(), item.GetStickerMessage(), item.GetPoll()
	sender := h.aciOf(ctx, item.GetAuthorId())
	if (sm == nil && st == nil && poll == nil) || sender == uuid.Nil {
		return nil, false
	}
	data := &signalpb.DataMessage{Timestamp: new(item.GetDateSent())}
	if text := sm.GetText(); text != nil {
		data.Body = new(text.GetBody())
		data.BodyRanges = archivedRanges(text.GetBodyRanges())
	}
	var hashes [][]byte
	for _, att := range sm.GetAttachments() {
		if p := attachment(att); p != nil {
			data.Attachments = append(data.Attachments, p)
			hashes = append(hashes, att.GetPointer().GetLocatorInfo().GetPlaintextHash())
		}
	}
	if sticker, hash := archivedSticker(st.GetSticker()); sticker != nil {
		data.Sticker = sticker
		if sticker.GetData() != nil {
			hashes = append(hashes, hash)
		}
	}
	if q := sm.GetQuote(); q.GetTargetSentTimestamp() != 0 {
		if author := h.aciOf(ctx, q.GetAuthorId()); author != uuid.Nil {
			data.Quote = &signalpb.DataMessage_Quote{
				Id: new(q.GetTargetSentTimestamp()), AuthorAci: new(author.String()), Text: new(q.GetText().GetBody()),
			}
		}
	}
	info := events.MessageInfo{ChatID: chat, Sender: sender, ServerTimestamp: item.GetDateSent()}
	read := true
	if in := item.GetIncoming(); in != nil {
		info.ServerTimestamp = max(in.GetDateServerSent(), in.GetDateReceived())
		read = in.GetRead()
	}
	m := h.a.message(ctx, info, data)
	m.Group = group
	m.Timer = uint32(item.GetExpiresInMs() / 1000) //nolint:gosec // Signal's timers fit
	m.TimerStarted = item.GetExpireStartDate()
	m.Poll = h.archivedPoll(ctx, poll)
	if m.GetBody() == "" && len(m.GetAttachments()) == 0 && m.GetPoll() == nil {
		return nil, false
	}
	for i := range m.GetAttachments() {
		if i < len(hashes) {
			m.Attachments[i].PlaintextHash = hashes[i]
		}
	}
	m.Reactions = h.reactions(ctx, slices.Concat(sm.GetReactions(), st.GetReactions(), poll.GetReactions()))
	return m, read
}

// archivedPoll is an archived poll with its votes: each voter's chosen answers and
// the number of their latest vote; nil for none.
func (h *history) archivedPoll(ctx context.Context, p *backuppb.Poll) *signalv1.Poll {
	if p == nil {
		return nil
	}
	out := &signalv1.Poll{Question: p.GetQuestion(), Multiple: p.GetAllowMultiple(), Closed: p.GetHasEnded()}
	ballots := map[string]*signalv1.Ballot{}
	var voters []string
	for i, o := range p.GetOptions() {
		out.Options = append(out.Options, o.GetOption())
		for _, v := range o.GetVotes() {
			voter := h.aciOf(ctx, v.GetVoterId())
			if voter == uuid.Nil {
				continue
			}
			b := ballots[voter.String()]
			if b == nil {
				b = &signalv1.Ballot{Voter: voter.String()}
				ballots[voter.String()] = b
				voters = append(voters, voter.String())
			}
			b.Options = append(b.Options, uint32(i))
			b.Round = max(b.GetRound(), v.GetVoteCount())
		}
	}
	for _, voter := range voters {
		out.Ballots = append(out.Ballots, ballots[voter])
	}
	return out
}

// archivedRanges is an archived body's mentions and styles as a message carries them;
// the archive numbers styles as messages do.
func archivedRanges(ranges []*backuppb.BodyRange) []*signalpb.BodyRange {
	var out []*signalpb.BodyRange
	for _, r := range ranges {
		at := &signalpb.BodyRange{Start: new(r.GetStart()), Length: new(r.GetLength())}
		switch aci, style := r.GetMentionAci(), r.GetStyle(); {
		case len(aci) == 16:
			at.AssociatedValue = &signalpb.BodyRange_MentionAci{MentionAci: uuid.UUID(aci).String()}
		case style != backuppb.BodyRange_NONE:
			at.AssociatedValue = &signalpb.BodyRange_Style_{Style: signalpb.BodyRange_Style(style)}
		default:
			continue
		}
		out = append(out, at)
	}
	return out
}

// archivedSticker is an archived sticker as a message carries it, with its picture's
// plaintext hash when the archive locates the picture; nil for none.
func archivedSticker(s *backuppb.Sticker) (*signalpb.DataMessage_Sticker, []byte) {
	if s == nil {
		return nil, nil
	}
	sticker := &signalpb.DataMessage_Sticker{
		PackId: s.GetPackId(), PackKey: s.GetPackKey(), StickerId: new(s.GetStickerId()), Emoji: new(s.GetEmoji()),
	}
	if p := filePointer(s.GetData()); p != nil {
		sticker.Data = p
		return sticker, s.GetData().GetLocatorInfo().GetPlaintextHash()
	}
	return sticker, nil
}

// reactions is an archived message's reactions by authors the account knows.
func (h *history) reactions(ctx context.Context, archived []*backuppb.Reaction) []*signalv1.Reaction {
	var out []*signalv1.Reaction
	for _, r := range archived {
		if author := h.aciOf(ctx, r.GetAuthorId()); author != uuid.Nil && r.GetEmoji() != "" {
			out = append(out, &signalv1.Reaction{Sender: author.String(), Emoji: r.GetEmoji()})
		}
	}
	return out
}

// attachment is an archived file as the pointer a message carries, as mautrix-signal
// reads one: where it is on the CDN while the transfer keeps it there, and its key; nil
// for one the archive does not locate.
func attachment(att *backuppb.MessageAttachment) *signalpb.AttachmentPointer {
	p := filePointer(att.GetPointer())
	if p == nil {
		return nil
	}
	switch att.GetFlag() {
	case backuppb.MessageAttachment_VOICE_MESSAGE:
		p.Flags = new(uint32(signalpb.AttachmentPointer_VOICE_MESSAGE))
	case backuppb.MessageAttachment_GIF:
		p.Flags = new(uint32(signalpb.AttachmentPointer_GIF))
	case backuppb.MessageAttachment_NONE, backuppb.MessageAttachment_BORDERLESS:
	}
	return p
}

// filePointer is an archived file's pointer: where it is on the CDN while the transfer
// keeps it there, and its key; nil for one the archive does not locate.
func filePointer(fp *backuppb.FilePointer) *signalpb.AttachmentPointer {
	li := fp.GetLocatorInfo()
	if li.GetTransitCdnKey() == "" {
		return nil
	}
	return &signalpb.AttachmentPointer{
		AttachmentIdentifier: &signalpb.AttachmentPointer_CdnKey{CdnKey: li.GetTransitCdnKey()},
		ContentType:          fp.ContentType, FileName: fp.FileName, Width: fp.Width, Height: fp.Height,
		Size: new(li.GetSize()), Digest: li.GetEncryptedDigest(), CdnNumber: li.TransitCdnNumber, Key: li.GetKey(),
	}
}
