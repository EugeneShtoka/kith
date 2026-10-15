package linkedin

import (
	"slices"
	"strings"
	"time"

	"go.mau.fi/mautrix-linkedin/pkg/linkedingo"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The inbox is what one connection knows of the account's conversations and the people
// in them. LinkedIn sends a conversation whole (its participants and newest messages),
// in listings and in live events alike, so each one is taken as it comes.

// categorySpam is the category of a conversation LinkedIn filed as spam.
const categorySpam = "SPAM"

// noteToSelf is the name of a conversation with yourself alone.
const noteToSelf = "Note to self"

// inbox is one connection's knowledge of its account. Not safe for concurrent use: the
// connection takes conversations in one at a time (conn.applying).
type inbox struct {
	self string
	// people are the names of everyone seen, by profile ID.
	people map[string]string
	// convs are the conversations shown, by thread.
	convs map[string]*conv
	// gone is when each conversation was deleted, so a listing fetched before the
	// deletion does not bring it back.
	gone map[string]time.Time
	// sources are the addresses of messages' attachments not yet cached (media.go).
	sources map[domain.EventID]string
}

// conv is one conversation as kith keeps it.
type conv struct {
	thread string
	title  string
	group  bool
	// others are the participants besides you, in LinkedIn's order.
	others []string
	// lastActive and readTo are LinkedIn's times; read is its own flag.
	lastActive, readTo time.Time
	read               bool
}

func newInbox(self string) *inbox {
	return &inbox{
		self: self, people: map[string]string{}, convs: map[string]*conv{}, gone: map[string]time.Time{},
		sources: map[domain.EventID]string{},
	}
}

// nameOfParticipant is how a participant reads: a member's first and last name, an
// organization's name.
func nameOfParticipant(p linkedingo.MessagingParticipant) string {
	switch t := p.ParticipantType; {
	case t.Member != nil:
		return strings.TrimSpace(t.Member.FirstName.Text + " " + t.Member.LastName.Text)
	case t.Organization != nil:
		return strings.TrimSpace(t.Organization.Name.Text)
	}
	return ""
}

// meet keeps a participant's name, reporting whether it is new or changed.
func (in *inbox) meet(p linkedingo.MessagingParticipant) bool {
	id, name := profileOf(p.EntityURN), nameOfParticipant(p)
	if id == "" || name == "" || in.people[id] == name {
		return false
	}
	in.people[id] = name
	return true
}

// take is one conversation from LinkedIn. It reports the conversation's thread and
// whether it is shown; a conversation not shown (spam, one you left, one deleted after
// this copy) is dropped from the inbox. met reports new or renamed people.
func (in *inbox) take(c *linkedingo.Conversation) (thread string, shown, met bool) {
	thread, ok := threadIn(c.EntityURN)
	if !ok {
		return "", false, false
	}
	member := false
	var others []string
	for _, p := range c.ConversationParticipants {
		met = in.meet(p) || met
		switch id := profileOf(p.EntityURN); {
		case id == in.self:
			member = true
		case id != "":
			others = append(others, id)
		}
	}
	deleted, wasDeleted := in.gone[thread]
	switch {
	case slices.Contains(c.Categories, categorySpam), !member && len(c.ConversationParticipants) > 0,
		wasDeleted && !c.LastActivityAt.After(deleted):
		delete(in.convs, thread)
		return thread, false, met
	}
	delete(in.gone, thread)
	cv := in.convs[thread]
	if cv == nil {
		cv = &conv{thread: thread}
		in.convs[thread] = cv
	}
	cv.title, cv.group, cv.read = c.Title, c.GroupChat, c.Read
	if len(c.ConversationParticipants) > 0 {
		cv.others = others
	}
	cv.lastActive = latest(cv.lastActive, c.LastActivityAt.Time)
	cv.readTo = latest(cv.readTo, c.LastReadAt.Time)
	return thread, true, met
}

// drop is a conversation LinkedIn deleted at.
func (in *inbox) drop(thread string, at time.Time) {
	delete(in.convs, thread)
	in.gone[thread] = latest(in.gone[thread], at)
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// room is a conversation as a kith room.
func (in *inbox) room(cv *conv) domain.Room {
	r := domain.Room{
		ID: roomID(in.self, cv.thread), Name: in.nameOf(cv), IsDirect: !cv.group,
		Membership: domain.MembershipJoin, Network: domain.ProtocolLinkedIn,
	}
	r.Members = append(r.Members, personID(in.self))
	for _, id := range cv.others {
		r.Members = append(r.Members, personID(id))
	}
	return r
}

// nameOf is a conversation's name: its title, else its people's.
func (in *inbox) nameOf(cv *conv) string {
	if t := strings.TrimSpace(cv.title); t != "" {
		return t
	}
	if len(cv.others) == 0 {
		return noteToSelf
	}
	names := make([]string, 0, len(cv.others))
	for _, id := range cv.others {
		if n := in.people[id]; n != "" {
			names = append(names, n)
		}
	}
	return strings.Join(names, ", ")
}

// roomMembers is a conversation's people as room members, named where known.
func (in *inbox) roomMembers(cv *conv) []domain.Member {
	ids := append([]string{in.self}, cv.others...)
	out := make([]domain.Member, 0, len(ids))
	for _, id := range ids {
		out = append(out, domain.Member{UserID: personID(id), DisplayName: in.people[id]})
	}
	return out
}

// message is a LinkedIn message as kith's, in thread; false for what is not shown (a
// system notice, an empty message). A recalled message comes back redacted.
func (in *inbox) message(thread string, m *linkedingo.Message) (domain.Message, bool) {
	if m.MessageBodyRenderFormat == linkedingo.MessageBodyRenderFormatSystem || m.EntityURN.IsEmpty() {
		return domain.Message{}, false
	}
	sender := profileOf(m.Sender.EntityURN)
	in.meet(m.Sender)
	msg := domain.Message{
		ID: eventID(in.self, thread, m.EntityURN.ID()), RoomID: roomID(in.self, thread),
		Sender: personID(sender), SenderName: in.people[sender], Timestamp: m.DeliveredAt.Time,
		Edited: m.MessageBodyRenderFormat == linkedingo.MessageBodyRenderFormatEdited,
	}
	if m.MessageBodyRenderFormat == linkedingo.MessageBodyRenderFormatRecalled {
		msg.Redacted, msg.RedactedBy = true, msg.Sender
		return msg, true
	}
	parts := []string{}
	if text := plain(m.Body); strings.TrimSpace(text) != "" {
		parts = append(parts, text)
	}
	for i := range m.RenderContent {
		rc := &m.RenderContent[i]
		if reply := rc.RepliedMessageContent; reply != nil && !reply.OriginalMessage.EntityURN.IsEmpty() {
			msg.ReplyTo = eventID(in.self, thread, reply.OriginalMessage.EntityURN.ID())
			continue
		}
		// The first attachment is shown; any other is named.
		if media, url := attachment(rc); media != nil && msg.Media == nil {
			msg.Media = media
			if url != "" {
				in.sources[msg.ID] = url
			}
			continue
		}
		if part := renderLabel(*rc, in.people); part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 && msg.Media == nil {
		return domain.Message{}, false
	}
	msg.Body = strings.Join(parts, "\n")
	if msg.Body == "" {
		msg.Body = msg.Media.Name
	}
	msg.Mentioned = mentions(m.Body, in.self)
	return msg, true
}

// plain is attributed text as plain text: a link whose words are not its address
// keeps the address after them.
func plain(t linkedingo.AttributedText) string {
	runes := []rune(t.Text)
	type insert struct {
		at   int
		text string
	}
	var adds []insert
	for _, a := range t.Attributes {
		link := a.AttributeKind.Hyperlink
		end := a.Start + a.Length
		if link == nil || link.URL == "" || a.Start < 0 || end > len(runes) || a.Length <= 0 {
			continue
		}
		if words := string(runes[a.Start:end]); strings.TrimSpace(words) != link.URL {
			adds = append(adds, insert{at: end, text: " (" + link.URL + ")"})
		}
	}
	if len(adds) == 0 {
		return t.Text
	}
	slices.SortStableFunc(adds, func(x, y insert) int { return x.at - y.at })
	var b strings.Builder
	last := 0
	for _, add := range adds {
		b.WriteString(string(runes[last:add.at]))
		b.WriteString(add.text)
		last = add.at
	}
	b.WriteString(string(runes[last:]))
	return b.String()
}

// mentions reports whether text mentions profile self.
func mentions(t linkedingo.AttributedText, self string) bool {
	for _, a := range t.Attributes {
		if e := a.AttributeKind.Entity; e != nil && e.URN.ID() == self {
			return true
		}
	}
	return false
}

// renderLabel is what a message carries besides its text and the attachment it shows:
// a further attachment's label, a forwarded message quoted, a shared post's address.
func renderLabel(rc linkedingo.RenderContent, people map[string]string) string {
	switch {
	case rc.VectorImage != nil:
		return "[photo]"
	case rc.Video != nil:
		return "[video]"
	case rc.Audio != nil:
		return "[voice message]"
	case rc.ExternalMedia != nil:
		return "[GIF]"
	case rc.File != nil:
		if rc.File.Name != "" {
			return "[file: " + rc.File.Name + "]"
		}
		return "[file]"
	case rc.ForwardedMessage != nil:
		f := rc.ForwardedMessage
		who := nameOfParticipant(f.OriginalSender)
		if who == "" {
			who = people[profileOf(f.OriginalSender.EntityURN)]
		}
		body := strings.ReplaceAll(strings.TrimSpace(plain(f.ForwardedBody)), "\n", "\n> ")
		if who != "" {
			return "Forwarded from " + who + ":\n> " + body
		}
		return "Forwarded:\n> " + body
	case rc.HostURNData != nil && !rc.HostURNData.HostURN.IsEmpty():
		if rc.HostURNData.Type == "FEED_UPDATE" {
			return "https://www.linkedin.com/feed/update/" + rc.HostURNData.HostURN.String()
		}
		return "[" + strings.ToLower(strings.ReplaceAll(rc.HostURNData.Type, "_", " ")) + "]"
	}
	return ""
}
