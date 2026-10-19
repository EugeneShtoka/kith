package gmessages

import (
	"strings"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The inbox is what one connection knows of the phone's conversations and the people in
// them. The phone sends a conversation whole (its participants and state), in listings
// and in live events alike, and a message again whenever its status or text changes;
// each is taken as it comes.

// inbox is one connection's knowledge of its phone. Not safe for concurrent use: the
// connection takes events in one at a time (conn.applying).
type inbox struct {
	self string
	// people are the names of everyone seen, by kith ID; me is which of them you are.
	people map[string]string
	me     map[string]bool
	// convs are the conversations shown, by the phone's ID.
	convs map[string]*conv
	// gone is when each conversation went away (deleted, spam, blocked), so a listing
	// fetched before does not bring it back.
	gone map[string]time.Time
}

// conv is one conversation as kith keeps it.
type conv struct {
	id    string
	name  string
	group bool
	// members are the participants by the phone's participant ID, as kith IDs.
	members map[string]string
	order   []string
	// lastActive is the newest message's time; readTo how far it is read, as the phone
	// says (it says only read or not, so read means read through the newest).
	lastActive, readTo time.Time
	unread             bool
	// kind and mode are how the phone sends there (SMS or RCS, and whether it holds to
	// SMS); outgoing is the participant you send as, sim the SIM it is on when the
	// phone says.
	kind     gmproto.ConversationType
	mode     gmproto.ConversationSendMode
	outgoing string
	sim      *gmproto.SIMCard
	// encrypted is whether the chat last switched to end-to-end encrypted RCS, as of
	// switchedAt, the newest such notice seen.
	encrypted  bool
	switchedAt time.Time
}

// sms reports whether a message sent to cv goes out as SMS or MMS: an SMS chat, or
// one the phone holds to SMS.
func (cv *conv) sms() bool {
	return cv.kind == gmproto.ConversationType_SMS || cv.mode != gmproto.ConversationSendMode_SEND_MODE_AUTO
}

// forceRCS reports whether a send to cv must not fall back to SMS: an RCS chat sent to
// as the phone chooses, that switched to end-to-end encryption (as mautrix-gmessages
// decides it).
func (cv *conv) forceRCS() bool {
	return cv.kind == gmproto.ConversationType_RCS && cv.mode == gmproto.ConversationSendMode_SEND_MODE_AUTO && cv.encrypted
}

func newInbox(self string) *inbox {
	return &inbox{
		self: self, people: map[string]string{}, me: map[string]bool{},
		convs: map[string]*conv{}, gone: map[string]time.Time{},
	}
}

// micros is a time the phone sends in microseconds.
func micros(us int64) time.Time {
	if us <= 0 {
		return time.Time{}
	}
	return time.UnixMicro(us)
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// shown reports whether a conversation in status is shown: archived ones are, spam,
// blocked and deleted ones are not.
func shown(status gmproto.ConversationStatus) bool {
	switch status {
	case gmproto.ConversationStatus_SPAM_FOLDER, gmproto.ConversationStatus_BLOCKED_FOLDER,
		gmproto.ConversationStatus_DELETED, gmproto.ConversationStatus_TRASH_FOLDER:
		return false
	default:
		return true
	}
}

// nameOf is how a participant reads: their contact name, else their number.
func nameOf(p *gmproto.Participant) string {
	for _, n := range []string{p.GetFullName(), p.GetFormattedNumber(), p.GetID().GetNumber()} {
		if n = strings.TrimSpace(n); n != "" {
			return n
		}
	}
	return ""
}

// take is one conversation from the phone. It reports whether it is shown (a
// conversation not shown is dropped from the inbox) and whether anyone new was met.
func (in *inbox) take(c *gmproto.Conversation) (shownNow, met bool) {
	id := c.GetConversationID()
	if id == "" {
		return false, false
	}
	members := map[string]string{}
	var order []string
	for _, p := range c.GetParticipants() {
		pid := p.GetID().GetParticipantID()
		person := personOf(in.self, p.GetID())
		if name := nameOf(p); name != "" && in.people[person] != name {
			in.people[person] = name
			met = true
		}
		if p.GetIsMe() {
			in.me[person] = true
		}
		if pid != "" {
			members[pid] = person
			order = append(order, pid)
		}
	}
	active := micros(c.GetLastMessageTimestamp())
	if deleted, was := in.gone[id]; !shown(c.GetStatus()) || (was && !active.After(deleted)) {
		if !shown(c.GetStatus()) {
			in.gone[id] = latest(in.gone[id], time.Now())
		}
		delete(in.convs, id)
		return false, met
	}
	delete(in.gone, id)
	cv := in.convs[id]
	if cv == nil {
		cv = &conv{id: id}
		in.convs[id] = cv
	}
	cv.name, cv.group = strings.TrimSpace(c.GetName()), c.GetIsGroupChat()
	if len(members) > 0 {
		cv.members, cv.order = members, order
	}
	cv.kind, cv.mode, cv.outgoing, cv.sim = c.GetType(), c.GetSendMode(), c.GetDefaultOutgoingID(), c.GetSimCard()
	cv.lastActive = latest(cv.lastActive, active)
	cv.unread = c.GetUnread()
	if !cv.unread {
		cv.readTo = latest(cv.readTo, active)
	}
	return true, met
}

// others is a conversation's people besides you, in the phone's order.
func (in *inbox) others(cv *conv) []string {
	var out []string
	for _, pid := range cv.order {
		if person := cv.members[pid]; !in.me[person] {
			out = append(out, person)
		}
	}
	return out
}

// room is a conversation as a kith room.
func (in *inbox) room(cv *conv) domain.Room {
	r := domain.Room{
		ID: roomID(in.self, cv.id), Name: in.roomName(cv), IsDirect: !cv.group,
		Membership: domain.MembershipJoin, Network: domain.ProtocolGMessages, SMS: cv.sms(),
	}
	r.Members = append(r.Members, in.membersOf(cv)...)
	return r
}

// membersOf is a conversation's people, once each in the phone's order: one person can be
// two participants (your own number, as you and as the SIM's line).
func (in *inbox) membersOf(cv *conv) []string {
	seen := make(map[string]bool, len(cv.order))
	out := make([]string, 0, len(cv.order))
	for _, pid := range cv.order {
		if person := cv.members[pid]; !seen[person] {
			seen[person] = true
			out = append(out, person)
		}
	}
	return out
}

// roomName is a conversation's name: the phone's, else its people's.
func (in *inbox) roomName(cv *conv) string {
	if cv.name != "" {
		return cv.name
	}
	var names []string
	for _, person := range in.others(cv) {
		if n := in.people[person]; n != "" {
			names = append(names, n)
		}
	}
	return strings.Join(names, ", ")
}

// roomMembers is a conversation's people as room members, named where known.
func (in *inbox) roomMembers(cv *conv) []domain.Member {
	people := in.membersOf(cv)
	out := make([]domain.Member, 0, len(people))
	for _, person := range people {
		out = append(out, domain.Member{UserID: person, DisplayName: in.people[person]})
	}
	return out
}

// deleted reports whether a message status means the message was deleted.
func deleted(status gmproto.MessageStatusType) bool {
	switch status {
	case gmproto.MessageStatusType_MESSAGE_DELETED, gmproto.MessageStatusType_OUTGOING_DELETED,
		gmproto.MessageStatusType_INCOMING_DELETED:
		return true
	default:
		return false
	}
}

// notice reports whether a status is the phone's notice about the conversation
// ("RCS chat started", "Dana left"), not a message.
func notice(status gmproto.MessageStatusType) bool {
	return status >= gmproto.MessageStatusType_TOMBSTONE_PARTICIPANT_JOINED && status < gmproto.MessageStatusType_MESSAGE_DELETED
}

// switched takes the phone's notice that a chat switched to or from end-to-end
// encrypted RCS; the newest notice stands, in whatever order they come.
func (in *inbox) switched(m *gmproto.Message) {
	var on bool
	switch m.GetMessageStatus().GetStatus() {
	case gmproto.MessageStatusType_MESSAGE_STATUS_TOMBSTONE_PROTOCOL_SWITCH_RCS_TO_E2EE,
		gmproto.MessageStatusType_MESSAGE_STATUS_TOMBSTONE_PROTOCOL_SWITCH_TEXT_TO_E2EE:
		on = true
	case gmproto.MessageStatusType_MESSAGE_STATUS_TOMBSTONE_PROTOCOL_SWITCH_E2EE_TO_RCS,
		gmproto.MessageStatusType_MESSAGE_STATUS_TOMBSTONE_PROTOCOL_SWITCH_E2EE_TO_TEXT:
	default:
		return
	}
	cv := in.convs[m.GetConversationID()]
	at := micros(m.GetTimestamp())
	if cv == nil || at.Before(cv.switchedAt) {
		return
	}
	cv.encrypted, cv.switchedAt = on, at
}

// notSent reports whether the phone could not send an outgoing message.
func notSent(status gmproto.MessageStatusType) bool {
	switch status {
	case gmproto.MessageStatusType_OUTGOING_FAILED_GENERIC, gmproto.MessageStatusType_OUTGOING_FAILED_EMERGENCY_NUMBER,
		gmproto.MessageStatusType_OUTGOING_CANCELED, gmproto.MessageStatusType_OUTGOING_FAILED_TOO_LARGE,
		gmproto.MessageStatusType_OUTGOING_FAILED_RECIPIENT_LOST_RCS, gmproto.MessageStatusType_OUTGOING_FAILED_NO_RETRY_NO_FALLBACK,
		gmproto.MessageStatusType_OUTGOING_FAILED_RECIPIENT_DID_NOT_DECRYPT, gmproto.MessageStatusType_OUTGOING_FAILED_RECIPIENT_LOST_ENCRYPTION,
		gmproto.MessageStatusType_OUTGOING_FAILED_RECIPIENT_DID_NOT_DECRYPT_NO_MORE_RETRY,
		gmproto.MessageStatusType_OUTGOING_FAILED_RECIPIENT_NEGATIVE_DELIVERY,
		gmproto.MessageStatusType_MESSAGE_STATUS_OUTGOING_FAILED_EMERGENCY_PROTOCOL_DETERMINATION_MESSAGE,
		gmproto.MessageStatusType_OUTGOING_FAILED_TO_ENCRYPT:
		return true
	default:
		return false
	}
}

// notDownloaded reports whether an MMS's content is still on the carrier's side.
func notDownloaded(status gmproto.MessageStatusType) bool {
	switch status {
	case gmproto.MessageStatusType_INCOMING_YET_TO_MANUAL_DOWNLOAD, gmproto.MessageStatusType_INCOMING_RETRYING_MANUAL_DOWNLOAD,
		gmproto.MessageStatusType_INCOMING_MANUAL_DOWNLOADING, gmproto.MessageStatusType_INCOMING_RETRYING_AUTO_DOWNLOAD,
		gmproto.MessageStatusType_INCOMING_AUTO_DOWNLOADING, gmproto.MessageStatusType_INCOMING_DOWNLOAD_FAILED,
		gmproto.MessageStatusType_INCOMING_EXPIRED_OR_NOT_AVAILABLE, gmproto.MessageStatusType_INCOMING_AWAITING_AUTO_DOWNLOAD:
		return true
	default:
		return false
	}
}

// sender is who sent a message in cv: by its participant ID, else the participant it
// carries.
func (in *inbox) sender(cv *conv, m *gmproto.Message) string {
	if cv != nil {
		if person := cv.members[m.GetParticipantID()]; person != "" {
			return person
		}
	}
	if p := m.GetSenderParticipant(); p != nil && p.GetID() != nil {
		person := personOf(in.self, p.GetID())
		if name := nameOf(p); name != "" {
			in.people[person] = name
		}
		if p.GetIsMe() {
			in.me[person] = true
		}
		return person
	}
	return domain.NativePerson(domain.ProtocolGMessages, in.self+"."+m.GetParticipantID())
}

// message is the phone's message as kith's; false for what is not shown (the phone's
// notices, an empty message). A deleted message comes back redacted.
func (in *inbox) message(m *gmproto.Message) (domain.Message, bool) {
	status := m.GetMessageStatus().GetStatus()
	if m.GetMessageID() == "" || m.GetConversationID() == "" || notice(status) {
		return domain.Message{}, false
	}
	cv := in.convs[m.GetConversationID()]
	sender := in.sender(cv, m)
	msg := domain.Message{
		ID: eventID(in.self, m.GetConversationID(), m.GetMessageID()), RoomID: roomID(in.self, m.GetConversationID()),
		Sender: sender, SenderName: in.people[sender], Timestamp: micros(m.GetTimestamp()),
	}
	if deleted(status) {
		msg.Redacted, msg.RedactedBy = true, sender
		return msg, true
	}
	var parts []string
	if s := strings.TrimSpace(m.GetSubject()); s != "" {
		parts = append(parts, s)
	}
	for _, info := range m.GetMessageInfo() {
		switch {
		case info.GetMessageContent() != nil:
			if text := info.GetMessageContent().GetContent(); strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		case info.GetMediaContent() != nil:
			parts = append(parts, mediaLabel(info.GetMediaContent()))
		}
	}
	if notDownloaded(status) && len(parts) == 0 {
		parts = append(parts, "[MMS not downloaded yet]")
	}
	if len(parts) == 0 {
		return domain.Message{}, false
	}
	if notSent(status) {
		parts = append(parts, notSentLabel)
	}
	msg.Body = strings.Join(parts, "\n")
	if reply := m.GetReplyMessage().GetMessageID(); reply != "" {
		msg.ReplyTo = eventID(in.self, m.GetConversationID(), reply)
	}
	return msg, true
}

// notSentLabel ends a message the phone could not send.
const notSentLabel = "[not sent: the phone could not send it]"

// mediaLabel names an attachment until pictures and files are shown.
func mediaLabel(media *gmproto.MediaContent) string {
	mime := media.GetMimeType()
	switch {
	case strings.HasPrefix(mime, "image/gif"):
		return "[GIF]"
	case strings.HasPrefix(mime, "image/"):
		return "[photo]"
	case strings.HasPrefix(mime, "video/"):
		return "[video]"
	case strings.HasPrefix(mime, "audio/"):
		return "[voice message]"
	case strings.Contains(mime, "vcard"):
		return "[contact]"
	case media.GetMediaName() != "":
		return "[file: " + media.GetMediaName() + "]"
	}
	return "[attachment]"
}
