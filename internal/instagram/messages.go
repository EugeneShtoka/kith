package instagram

import (
	"cmp"
	"strconv"
	"strings"
	"unicode/utf16"

	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// fbid is a person's or thread's Facebook-side ID as kith writes it.
func fbid(id int64) string { return strconv.FormatInt(id, 10) }

// message is m, a message in the thread chat, as kith keeps it; ok is false for what
// kith does not show (admin notes Instagram's own apps keep quiet).
func message(self, chat string, m *slidetypes.Message) (domain.Message, bool) {
	if m == nil || m.ID == "" {
		return domain.Message{}, false
	}
	out := domain.Message{
		ID:        eventID(self, chat, m.ID),
		RoomID:    roomID(self, chat),
		Sender:    person(fbid(m.SenderFBID)),
		Timestamp: m.TimestampMS.Time,
	}
	if m.Sender != nil {
		out.SenderName = nameOf(&m.Sender.UserDict)
		if out.SenderName == "" {
			out.SenderName = m.Sender.Name
		}
	}
	if m.RepliedToMessageID != "" {
		out.ReplyTo = eventID(self, chat, m.RepliedToMessageID)
	}
	if c, ok := m.Content.Content.(*slidetypes.MessageContentText); ok {
		out.Body, out.Mentioned = mentioned(self, cmp.Or(c.TextBody, m.TextBody), m.Mentions)
		return out, true
	}
	if a, ok := m.Content.Content.(*slidetypes.MessageContentAdminText); ok {
		if m.ContentType != "IG_VIDEO_CALL_XMAT" {
			return domain.Message{}, false
		}
		out.Body = adminText(a, m.IGDSnippet)
		return out, true
	}
	out.Media, out.Body = described(m)
	return out, true
}

// described is what a message that is no plain text carries: its first file's media,
// and a body saying the rest.
func described(m *slidetypes.Message) (*domain.Media, string) {
	var media *domain.Media
	body := ""
	switch c := m.Content.Content.(type) {
	case *slidetypes.MessageContentImage:
		media, body = attachments(domain.MediaImage, c.Attachments, "")
	case *slidetypes.MessageContentMultiMedia:
		media, body = attachments(domain.MediaImage, c.Attachments, "")
	case *slidetypes.MessageContentVideo:
		media, body = attachments(domain.MediaVideo, c.Videos, "")
	case *slidetypes.MessageContentRavenImage:
		body = "[a photo that can only be seen in the Instagram app]"
	case *slidetypes.MessageContentRavenVideo:
		body = "[a video that can only be seen in the Instagram app]"
	case *slidetypes.MessageContentAudio:
		media = &domain.Media{Type: domain.MediaAudio, Name: "voice message"}
	case *slidetypes.MessageContentAnimatedMedia:
		body = "[GIF]"
		if len(c.AnimatedMedia) > 0 && c.AnimatedMedia[0].IsSticker {
			body = "[sticker]"
		}
		if len(c.AnimatedMedia) > 0 && c.AnimatedMedia[0].AltText != "" {
			body += " " + c.AnimatedMedia[0].AltText
		}
	case *slidetypes.MessageContentSticker:
		body = strings.TrimSpace("[sticker] " + c.AltText)
	case *slidetypes.MessageContentMusicSticker:
		body = "[music]"
	case *slidetypes.MessageContentXMA:
		body = shared(c)
	default:
		body = cmp.Or(m.TextBody, m.IGDSnippet, "[a message kith cannot show; open it in the Instagram app]")
	}
	return media, body
}

// nameOf is a user's name: their full name, else their username.
func nameOf(u *slidetypes.User) string {
	if u == nil {
		return ""
	}
	return cmp.Or(strings.TrimSpace(u.FullName), u.Username)
}

// attachments describes a message's files: the first as its media (fetching them comes
// later), the rest counted in its body.
func attachments(kind domain.MediaType, files []*slidetypes.Attachment, body string) (*domain.Media, string) {
	if len(files) == 0 {
		return nil, cmp.Or(body, "[an attachment kith cannot show]")
	}
	m := &domain.Media{Type: kind, Width: files[0].PreviewWidth, Height: files[0].PreviewHeight}
	switch kind {
	case domain.MediaVideo:
		m.Name = "video"
	default:
		m.Name = "photo"
	}
	if more := len(files) - 1; more > 0 {
		body = strings.TrimSpace(body + "\n(+" + strconv.Itoa(more) + " more)")
	}
	return m, body
}

// shared is a shared post, story, reel or link as text: its title, what it says, and
// where it points.
func shared(c *slidetypes.MessageContentXMA) string {
	if c.XMA == nil {
		return cmp.Or(c.XMATextBody, "[something shared; open it in the Instagram app]")
	}
	x := c.XMA
	var parts []string
	for _, s := range []string{c.XMATextBody, cmp.Or(x.TitleText, x.XMATitle, x.HeaderTitleText), x.SubtitleText, x.CaptionBodyText, x.TargetURL} {
		if s = strings.TrimSpace(s); s != "" && !strings.Contains(strings.Join(parts, "\n"), s) {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return "[something shared; open it in the Instagram app]"
	}
	return strings.Join(parts, "\n")
}

// adminText is an admin note's words (a call's start or end).
func adminText(c *slidetypes.MessageContentAdminText, snippet string) string {
	var b strings.Builder
	for _, f := range c.TextFragments {
		b.WriteString(f.Plaintext)
	}
	return cmp.Or(strings.TrimSpace(b.String()), snippet)
}

// mentioned reports whether self is among a body's mentions (Instagram counts them in
// UTF-16 units over the body); the body keeps its "@username" text.
func mentioned(self, body string, mentions slidetypes.MentionList) (string, bool) {
	units := len(utf16.Encode([]rune(body)))
	for _, m := range mentions {
		if m != nil && fbid(m.UserFBID) == self && m.Offset >= 0 && m.Offset+m.Length <= units {
			return body, true
		}
	}
	return body, false
}
