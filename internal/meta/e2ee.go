package meta

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Messenger's end-to-end encrypted chats travel over the WhatsApp protocol. kith reads
// them as a device of its own on the account: registered once (Messenger lists it with
// your devices), kept in the Messenger store, and connected with whatsmeow once the
// main connection is up. An encrypted chat's ID is its thread's key, so its messages
// land in the same room. Only what arrives after the device was registered can be read:
// older encrypted history sits behind Messenger's PIN-protected backup.

// errNoStore is encrypted chats asked for with no store to keep a device in.
var errNoStore = errors.New("meta: the Messenger store did not open, so encrypted chats are off")

// connectE2EE connects c's encrypted chats, once at a time; a failure is logged and
// tried again on the next reconnect.
func (a *Adapter) connectE2EE(ctx context.Context, c *conn) {
	if a.devices == nil {
		a.log.Warn("encrypted chats are off", "account", c.account.Name, "err", errNoStore)
		return
	}
	c.mu.Lock()
	if c.e2ee != nil || c.e2eeBusy || c.client == nil {
		c.mu.Unlock()
		return
	}
	c.e2eeBusy = true
	client, s := c.client, c.session
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.e2eeBusy = false
		c.mu.Unlock()
	}()
	err := a.guard("connecting encrypted chats", func() error { return a.startE2EE(ctx, c, client, s) })
	if err != nil {
		a.log.Warn("connect encrypted chats failed; tried again on the next reconnect", "account", c.account.Name, "err", err)
	}
}

// startE2EE loads or registers the device and connects whatsmeow.
func (a *Adapter) startE2EE(ctx context.Context, c *conn, client *messagix.Client, s Session) error {
	dev, fresh, err := a.device(ctx, client, s)
	if err != nil {
		return err
	}
	client.SetDevice(dev)
	if fresh {
		a.log.Info("registering kith as a device for encrypted chats", "account", c.account.Name)
		if err = client.RegisterE2EE(ctx, s.MetaID); err != nil {
			return fmt.Errorf("meta: register the device: %w", err)
		}
		if err = dev.Save(ctx); err != nil {
			return fmt.Errorf("meta: keep the device: %w", err)
		}
		c.mu.Lock()
		c.session.WADevice = dev.ID.Device
		c.mu.Unlock()
		a.keepCookies(c) // saves the session with the device's number
	}
	wa, err := client.PrepareE2EEClient()
	if err != nil {
		return fmt.Errorf("meta: prepare the encrypted connection: %w", err)
	}
	wa.AddEventHandler(func(evt any) { a.onEncrypted(c, evt) })
	if err := wa.Connect(); err != nil {
		return fmt.Errorf("meta: connect the encrypted connection: %w", err)
	}
	c.mu.Lock()
	c.e2ee = wa
	c.mu.Unlock()
	if c.closed() {
		wa.Disconnect()
	}
	return nil
}

// device is the account's device for encrypted chats: the kept one, or a new one to
// register (fresh).
func (a *Adapter) device(ctx context.Context, client *messagix.Client, s Session) (dev *store.Device, fresh bool, err error) {
	if s.WADevice != 0 {
		dev, err = a.devices.container.GetDevice(ctx, types.JID{User: itoa(s.MetaID), Device: s.WADevice, Server: types.MessengerServer})
		if err != nil {
			return nil, false, fmt.Errorf("meta: load the device: %w", err)
		}
		if dev != nil {
			return dev, false, nil
		}
		a.log.Warn("the encrypted chats' device is not in the store; registering a new one", "meta_id", s.MetaID)
	}
	dev = a.devices.container.NewDevice()
	switch {
	case s.Native != nil:
		dev.FacebookUUID = s.Native.DeviceID
	case client.MessengerLite != nil:
		dev.FacebookUUID = client.MessengerLite.GetSuggestedDeviceID()
	}
	return dev, true, nil
}

// forgetDevice drops c's device after Meta refused it, so the next connect registers
// a new one.
func (a *Adapter) forgetDevice(c *conn) {
	c.mu.Lock()
	wa := c.e2ee
	c.e2ee = nil
	c.session.WADevice = 0
	c.mu.Unlock()
	if wa != nil {
		wa.Disconnect()
		if wa.Store != nil {
			if err := wa.Store.Delete(context.Background()); err != nil {
				a.log.Warn("delete the refused device failed", "account", c.account.Name, "err", err)
			}
		}
	}
	a.keepCookies(c)
}

// onEncrypted is whatsmeow's events for c.
func (a *Adapter) onEncrypted(c *conn, evt any) {
	if c.closed() {
		return
	}
	err := a.guard("handling an encrypted event", func() error {
		switch e := evt.(type) {
		case *events.FBMessage:
			a.takeEncrypted(context.Background(), c, e)
		case *events.Receipt:
			a.onEncryptedReceipt(context.Background(), c, e)
		case *events.Connected:
			a.log.Info("encrypted chats connected", "account", c.account.Name)
			go a.sendOwed(c)
		case *events.ConnectFailure:
			a.log.Warn("Meta refused the encrypted chats' device", "account", c.account.Name, "reason", e.Reason)
			if e.Reason == events.ConnectFailureNotFound || e.Reason == events.ConnectFailureClientUnknown ||
				e.Reason == events.ConnectFailureLoggedOut {
				a.forgetDevice(c)
				go a.connectE2EE(context.Background(), c)
			}
		case *events.LoggedOut:
			a.log.Warn("Meta logged the encrypted chats' device out", "account", c.account.Name, "reason", e.Reason)
			a.forgetDevice(c)
			go a.connectE2EE(context.Background(), c)
		case *events.CATRefreshError:
			a.log.Warn("the encrypted connection's token could not be renewed", "account", c.account.Name, "err", e.Error)
		}
		return nil
	})
	if err != nil {
		a.log.Error("an encrypted event was dropped", "account", c.account.Name, "err", err)
	}
}

// chatThread is the thread key of an encrypted chat: its JID's user, which is the
// thread's key (a one-to-one chat's is the other person's Meta ID).
func chatThread(chat types.JID) (int64, bool) {
	key, err := strconv.ParseInt(chat.User, 10, 64)
	return key, err == nil && key != 0
}

// takeEncrypted takes one encrypted message in: a new message, an edit or an unsend.
func (a *Adapter) takeEncrypted(ctx context.Context, c *conn, e *events.FBMessage) {
	key, ok := chatThread(e.Info.Chat)
	if !ok {
		return
	}
	c.applying.Lock()
	defer c.applying.Unlock()
	in := c.inbox
	// A chat not heard of yet becomes a thread at once, named once its row comes.
	if in.threads[key] == nil {
		kind := table.ENCRYPTED_OVER_WA_GROUP
		if e.Info.Chat.Server == types.MessengerServer {
			kind = table.ENCRYPTED_OVER_WA_ONE_TO_ONE
		}
		ch := change{read: map[int64]int64{}, left: map[string]int{}}
		in.upsert(key, kind, "", "inbox", e.Info.Timestamp.UnixMilli(), 0, -1, &ch)
		a.store(ctx, c, ch)
	}
	sender, _ := strconv.ParseInt(e.Info.Sender.ToNonAD().User, 10, 64)
	room := roomID(in.self, key)
	ca := e.GetConsumerApplication()
	if revoked := ca.GetPayload().GetApplicationData().GetRevoke(); revoked != nil {
		a.unsent(ctx, c, room, eventID(in.self, key, revoked.GetKey().GetID()), personID(sender), e.Info.Timestamp)
		return
	}
	content := ca.GetPayload().GetContent()
	if edit := content.GetEditMessage(); edit != nil {
		a.edited(ctx, c, domain.Message{
			ID: eventID(in.self, key, edit.GetKey().GetID()), RoomID: room, Sender: personID(sender),
			SenderName: in.people[sender], Body: edit.GetMessage().GetText(), Edited: true,
			RevisionID: eventID(in.self, key, e.Info.ID), Timestamp: e.Info.Timestamp, EditedAt: e.Info.Timestamp,
		})
		return
	}
	if r := content.GetReactionMessage(); r != nil {
		if reaction, ok := in.reaction(key, r.GetKey().GetID(), sender, r.GetText()); ok {
			a.applyReaction(ctx, reaction)
		}
		return
	}
	body, media, source := encryptedShown(ca, content)
	if strings.TrimSpace(body) == "" {
		return
	}
	msg := domain.Message{
		ID: eventID(in.self, key, e.Info.ID), RoomID: room, Sender: personID(sender),
		SenderName: in.people[sender], Body: body, Timestamp: e.Info.Timestamp, Media: media,
	}
	if media != nil {
		in.sources[msg.ID] = source
	}
	if quoted := e.FBApplication.GetMetadata().GetQuotedMessage(); quoted.GetStanzaID() != "" {
		msg.ReplyTo = eventID(in.self, key, quoted.GetStanzaID())
	}
	if !in.active(key, e.Info.Timestamp.UnixMilli()) {
		return // a chat you left, and this from before
	}
	// Your own message needs no read mark: unread counts start after your newest message.
	counted := map[domain.RoomID]bool{room: true}
	a.cacheMessages(ctx, c, map[domain.RoomID][]domain.Message{room: {msg}}, []domain.Message{msg}, counted)
	a.recount(ctx, room)
}

// encryptedShown is what an encrypted message shows: its attachment, with its caption
// (or the attachment's name) as its text, or its text alone.
func encryptedShown(ca *waConsumerApplication.ConsumerApplication, content *waConsumerApplication.ConsumerApplication_Content) (string, *domain.Media, mediaSource) {
	if ca == nil {
		return "[a message only the Messenger app shows]", nil, mediaSource{}
	}
	if media, source, caption := encryptedMedia(content); media != nil {
		if caption == "" {
			caption = media.Name
		}
		return caption, media, source
	}
	return encryptedBody(content), nil, mediaSource{}
}

// encryptedBody is what an encrypted message shows: its text, or a label and caption
// for what it carries until files and pictures are shown.
func encryptedBody(c *waConsumerApplication.ConsumerApplication_Content) string {
	captioned := func(label, caption string) string {
		if caption = strings.TrimSpace(caption); caption != "" {
			return caption + "\n" + label
		}
		return label
	}
	switch {
	case c.GetMessageText() != nil:
		return c.GetMessageText().GetText()
	case c.GetExtendedTextMessage() != nil:
		return c.GetExtendedTextMessage().GetText().GetText()
	case c.GetImageMessage() != nil:
		return captioned("[photo]", c.GetImageMessage().GetCaption().GetText())
	case c.GetVideoMessage() != nil:
		return captioned("[video]", c.GetVideoMessage().GetCaption().GetText())
	case c.GetAudioMessage() != nil:
		return "[voice message]"
	case c.GetDocumentMessage() != nil:
		return "[file: " + c.GetDocumentMessage().GetFileName() + "]"
	case c.GetStickerMessage() != nil:
		return "[sticker]"
	case c.GetViewOnceMessage() != nil:
		return "[view-once media: open it in the Messenger app]"
	case c.GetLocationMessage() != nil:
		loc := c.GetLocationMessage()
		return strings.TrimSpace("[location] " + loc.GetLocation().GetName() + " " + loc.GetAddress())
	case c.GetLiveLocationMessage() != nil:
		return "[live location]"
	case c.GetContactMessage() != nil, c.GetContactsArrayMessage() != nil:
		return "[contact]"
	case c.GetPollCreationMessage() != nil:
		return "[poll]"
	case c.GetGroupInviteMessage() != nil:
		return "[group invite]"
	}
	return ""
}

// edited replaces a message's text, keeping what it replaced under [display.deleted]
// keep.
func (a *Adapter) edited(ctx context.Context, c *conn, edit domain.Message) {
	if strings.TrimSpace(edit.Body) == "" || a.cache == nil {
		return
	}
	// The cache keeps the message's first time and its reply: an edit keeps its place.
	save := a.cache.SaveMessages
	if a.keepsDeleted() {
		save = a.cache.SaveMessagesWithRevisions
	}
	if err := save(ctx, edit.RoomID, []domain.Message{edit}); err != nil {
		a.log.Warn("cache an edit failed", "account", c.account.Name, "err", err)
		return
	}
	emit(a, a.messages, edit)
}

// unsent marks a message unsent for everyone, its text kept only under
// [display.deleted] keep.
func (a *Adapter) unsent(ctx context.Context, c *conn, room domain.RoomID, id domain.EventID, by string, at time.Time) {
	if a.cache == nil {
		return
	}
	if err := a.cache.MarkRedacted(ctx, room, id, by, "", at, a.keepsDeleted()); err != nil {
		a.log.Warn("mark a message unsent failed", "account", c.account.Name, "err", err)
		return
	}
	emit(a, a.messages, domain.Message{ID: id, RoomID: room, Redacted: true, RedactedBy: by, RedactedAt: at})
	a.recount(ctx, room)
}

// onEncryptedReceipt moves a chat's read position when you read it on another device;
// others' receipts are not kept yet.
func (a *Adapter) onEncryptedReceipt(ctx context.Context, c *conn, e *events.Receipt) {
	if e.Type != types.ReceiptTypeReadSelf {
		return
	}
	key, ok := chatThread(e.Chat)
	if !ok {
		return
	}
	room := roomID(c.metaID, key)
	a.readTo(ctx, room, e.Timestamp)
	a.recount(ctx, room)
}

// readEncrypted tells Meta an encrypted chat is read through at: a read receipt for
// the newest message by someone else at or before it.
func (a *Adapter) readEncrypted(ctx context.Context, c *conn, wa *whatsmeow.Client, thread, at int64, group bool) error {
	if a.cache == nil {
		return nil
	}
	room := roomID(c.metaID, thread)
	msgs, err := a.cache.Messages(ctx, room, readScan)
	if err != nil {
		return fmt.Errorf("meta: read %s's messages: %w", room, err)
	}
	mid, sender, ok := receiptTarget(msgs, c.metaID, at)
	if !ok {
		return nil
	}
	chat := types.JID{User: itoa(thread), Server: types.MessengerServer}
	if group {
		chat.Server = types.GroupServer
	}
	return wa.MarkRead(ctx, []types.MessageID{mid}, time.Now(), chat, //nolint:wrapcheck // the caller logs it with what it was for
		types.JID{User: sender, Server: types.MessengerServer})
}

// receiptTarget is the message a read receipt through at names: the newest one by
// someone else, sent at or before at, not unsent. msgs are oldest first.
func receiptTarget(msgs []domain.Message, self, at int64) (mid, sender string, ok bool) {
	for i := range slices.Backward(msgs) {
		m := &msgs[i]
		if m.Sender == personID(self) || m.Timestamp.UnixMilli() > at || m.Redacted {
			continue
		}
		_, mid, cut := strings.Cut(domain.ParseID(string(m.ID)).Native, "/")
		sender = domain.ParseID(m.Sender).Native
		return mid, sender, cut && mid != "" && sender != ""
	}
	return "", "", false
}

// readScan is how many of a chat's newest messages a read receipt looks through.
const readScan = 50
