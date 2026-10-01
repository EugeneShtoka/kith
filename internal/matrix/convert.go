package matrix

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// eventTime converts ms to a time, keeping 0 as the zero time rather than 1970.
func eventTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// redactionReason is the reason a redaction gave, usually "".
func redactionReason(evt *event.Event) string {
	if evt.Content.Parsed == nil {
		return ""
	}
	redaction, ok := evt.Content.Parsed.(*event.RedactionEventContent)
	if !ok {
		return ""
	}
	return redaction.Reason
}

// mediaSource extracts an attachment's mxc URI and, when encrypted, its m.file JSON.
func mediaSource(content *event.MessageEventContent) (mxc, fileJSON string) {
	if content.File != nil {
		if data, err := json.Marshal(content.File); err == nil {
			fileJSON = string(data)
		}
		return string(content.File.URL), fileJSON
	}
	return string(content.URL), ""
}

// toReaction extracts an m.reaction (annotation) event into a domain.Reaction.
// ok is false for a non-annotation relation or a missing target/key.
func toReaction(evt *event.Event) (domain.Reaction, bool) {
	rc := evt.Content.AsReaction()
	if rc == nil {
		return domain.Reaction{}, false
	}
	rel := rc.RelatesTo
	target, key := rel.GetAnnotationID(), rel.GetAnnotationKey()
	if target == "" || key == "" {
		return domain.Reaction{}, false
	}
	return domain.Reaction{
		ID:     domain.EventID(evt.ID),
		RoomID: domain.RoomID(evt.RoomID),
		Target: domain.EventID(target),
		Sender: string(evt.Sender),
		Key:    key,
	}, true
}

// mediaType maps an attachment msgtype to a domain media kind ("" when none).
func mediaType(t event.MessageType) domain.MediaType {
	switch t {
	case event.MsgImage:
		return domain.MediaImage
	case event.MsgVideo:
		return domain.MediaVideo
	case event.MsgAudio:
		return domain.MediaAudio
	case event.MsgFile:
		return domain.MediaFile
	default:
		return ""
	}
}

// buildMedia extracts attachment metadata, or nil for no attachment. The name comes
// from `filename` (MSC2530: body may be a caption), via GetFileName. The event type
// matters because m.sticker has no msgtype but is an image.
func buildMedia(evtType event.Type, content *event.MessageEventContent) *domain.Media {
	mt := mediaType(content.MsgType)
	if mt == "" && evtType == event.EventSticker {
		mt = domain.MediaImage
	}
	if mt == "" {
		return nil
	}
	m := &domain.Media{Type: mt, Name: content.GetFileName()}
	if content.Info != nil {
		m.Mime = content.Info.MimeType
		m.Width, m.Height, m.Size = content.Info.Width, content.Info.Height, content.Info.Size
	}
	return m
}

// toDomainMessage translates a message event into a domain.Message. ok is false
// for anything without renderable content (AsMessage never returns nil).
func toDomainMessage(evt *event.Event) (domain.Message, bool) {
	content := evt.Content.AsMessage()
	// An edit (m.replace) folds onto its target, carrying the replacement body.
	if target := content.RelatesTo.GetReplaceID(); target != "" {
		body := editBody(content)
		if body == "" {
			return domain.Message{}, false
		}
		return domain.Message{
			ID:     domain.EventID(target),
			RoomID: domain.RoomID(evt.RoomID),
			Sender: string(evt.Sender),
			Body:   body,
			// The replacement's own formatting; without it the edit reads as plain.
			Format: editFormatting(content),
			// See domain.Message.RevisionID.
			RevisionID: domain.EventID(evt.ID),
			Timestamp:  time.UnixMilli(evt.Timestamp),
			EditedAt:   time.UnixMilli(evt.Timestamp),
			Edited:     true,
		}, true
	}
	// Attachments may have an empty body; detect before the empty-body drop.
	media := buildMedia(evt.Type, content)
	if content.Body == "" && media == nil {
		// A redacted event from /messages: keep it as a "(deleted)" row.
		if because := evt.Unsigned.RedactedBecause; because != nil {
			return domain.Message{
				ID:             domain.EventID(evt.ID),
				RoomID:         domain.RoomID(evt.RoomID),
				Sender:         string(evt.Sender),
				Timestamp:      time.UnixMilli(evt.Timestamp),
				Redacted:       true,
				RedactedBy:     string(because.Sender),
				RedactedReason: redactionReason(because),
				RedactedAt:     eventTime(because.Timestamp),
			}, true
		}
		return domain.Message{}, false
	}
	// Strip the quoted reply fallback. GetNonFallbackReplyTo, not GetReplyTo: thread
	// replies carry a falling-back m.in_reply_to for thread-unaware clients, which
	// would turn a thread into a braid of quotes.
	body := content.Body
	threadRoot := content.RelatesTo.GetThreadParent()
	replyTo := content.RelatesTo.GetNonFallbackReplyTo()
	if replyTo != "" || threadRoot != "" {
		body = event.TrimReplyFallbackText(body)
	}
	return domain.Message{
		ID:         domain.EventID(evt.ID),
		RoomID:     domain.RoomID(evt.RoomID),
		Sender:     string(evt.Sender),
		Body:       body,
		Timestamp:  time.UnixMilli(evt.Timestamp),
		ReplyTo:    domain.EventID(replyTo),
		ThreadRoot: domain.EventID(threadRoot),
		Media:      media,
		Mentions:   parseMentions(content.FormattedBody),
		// Sanitized at ingest (untrusted HTML).
		Format: formatting(content),
		// m.notice stays text; an emote is an action, not speech.
		Emote: content.MsgType == event.MsgEmote,
	}, true
}

// editBody is an m.replace's new text: m.new_content, else the spec's "* " fallback.
func editBody(content *event.MessageEventContent) string {
	if content.NewContent != nil {
		return content.NewContent.Body
	}
	return strings.TrimPrefix(content.Body, "* ")
}

// editFormatting is an m.replace's new formatting, or none when the new content is
// plain (or absent: the "* " fallback is plain text).
func editFormatting(content *event.MessageEventContent) richtext.Formatted {
	if content.NewContent == nil {
		return richtext.Formatted{}
	}
	return formatting(content.NewContent)
}

// formatting is a message's formatted body, without the reply fallback, as kith's
// markup: Matrix's HTML, sanitized (it is untrusted). None when it has no HTML.
func formatting(content *event.MessageEventContent) richtext.Formatted {
	if content.Format != event.FormatHTML {
		return richtext.Formatted{}
	}
	return richtext.FromMarkup(richtext.Sanitize(replyFallback.ReplaceAllString(content.FormattedBody, "")))
}

// pillRe matches a matrix.to mention pill, capturing the MXID and display text.
var pillRe = regexp.MustCompile(`<a href="[^"]*matrix\.to/#/(@[^"?/]+:[^"?/]+)"[^>]*>([^<]+)</a>`)

// parseMentions extracts pill mentions (MXID and display text) from formatted HTML.
func parseMentions(formattedBody string) []domain.Mention {
	if formattedBody == "" {
		return nil
	}
	matches := pillRe.FindAllStringSubmatch(formattedBody, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]domain.Mention, 0, len(matches))
	for _, m := range matches {
		name := strings.TrimPrefix(html.UnescapeString(m[2]), "@")
		if name == "" {
			continue
		}
		out = append(out, domain.Mention{UserID: m[1], Name: name})
	}
	return out
}

// mentionsMe reports whether evt mentions me, via m.mentions or a pill.
func mentionsMe(evt *event.Event, me id.UserID) bool {
	if me == "" {
		return false
	}
	content := evt.Content.AsMessage()
	if content == nil {
		return false
	}
	// m.mentions counts except on replies: the spec puts the replied-to sender there,
	// so every reply to you would claim to mention you. A reply that names you still
	// has a pill.
	if content.Mentions.Has(me) && content.RelatesTo.GetReplyTo() == "" {
		return true
	}
	// Otherwise a pill to me outside the reply fallback (whose quote links the
	// answered sender) counts. Same regexp the timeline draws pills with.
	body := replyFallback.ReplaceAllString(content.FormattedBody, "")
	for _, pill := range pillRe.FindAllStringSubmatch(body, -1) {
		if pill[1] == string(me) {
			return true
		}
	}
	return false
}

// replyFallback matches the <mx-reply> quote a rich reply prefixes its HTML with.
var replyFallback = regexp.MustCompile(`(?is)<mx-reply>.*?</mx-reply>`)

// redactionTarget returns the event a redaction removes. Room v11 carries the
// target in content; older rooms carry it as the top-level redacts field — the
// SDK populates evt.Redacts as the fallback.
func redactionTarget(evt *event.Event) domain.EventID {
	if r := evt.Content.AsRedaction(); r != nil && r.Redacts != "" {
		return domain.EventID(r.Redacts)
	}
	return domain.EventID(evt.Redacts)
}
