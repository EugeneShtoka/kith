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

// message is m, a message in the thread chat, as kith keeps it, and where its
// attachment is; ok is false for what kith does not show (admin notes Instagram's own
// apps keep quiet).
func message(self, chat string, m *slidetypes.Message) (domain.Message, mediaSource, bool) {
	if m == nil || m.ID == "" {
		return domain.Message{}, mediaSource{}, false
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
		return out, mediaSource{}, true
	}
	if a, ok := m.Content.Content.(*slidetypes.MessageContentAdminText); ok {
		if m.ContentType != "IG_VIDEO_CALL_XMAT" {
			return domain.Message{}, mediaSource{}, false
		}
		out.Body = adminText(a, m.IGDSnippet)
		return out, mediaSource{}, true
	}
	var source mediaSource
	out.Media, source, out.Body = described(m)
	return out, source, true
}

// described is what a message that is no plain text carries: its first file's media
// and where it is, and a body saying the rest. A view-once photo or video stays in
// the Instagram app.
func described(m *slidetypes.Message) (*domain.Media, mediaSource, string) {
	var media *domain.Media
	var source mediaSource
	body := ""
	switch c := m.Content.Content.(type) {
	case *slidetypes.MessageContentImage:
		media, source, body = attached(domain.MediaImage, c.Attachments, "")
	case *slidetypes.MessageContentMultiMedia:
		media, source, body = attached(domain.MediaImage, c.Attachments, "")
	case *slidetypes.MessageContentVideo:
		media, source, body = attached(domain.MediaVideo, c.Videos, "")
	case *slidetypes.MessageContentRavenImage:
		body = "[a photo that can only be seen in the Instagram app]"
	case *slidetypes.MessageContentRavenVideo:
		body = "[a video that can only be seen in the Instagram app]"
	case *slidetypes.MessageContentAudio:
		media, source = voice(c)
	case *slidetypes.MessageContentAnimatedMedia:
		media, source, body = animated(c)
	case *slidetypes.MessageContentSticker:
		media = &domain.Media{Type: domain.MediaImage, Name: "sticker", Width: c.PreviewWidth, Height: c.PreviewHeight}
		source, body = mediaSource{URL: c.PreviewURL}, c.AltText
	case *slidetypes.MessageContentMusicSticker:
		body = "[music]"
	case *slidetypes.MessageContentXMA:
		body = shared(c)
	default:
		body = cmp.Or(m.TextBody, m.IGDSnippet, "[a message kith cannot show; open it in the Instagram app]")
	}
	return media, source, body
}

// nameOf is a user's name: their full name, else their username.
func nameOf(u *slidetypes.User) string {
	if u == nil {
		return ""
	}
	return cmp.Or(strings.TrimSpace(u.FullName), u.Username)
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
