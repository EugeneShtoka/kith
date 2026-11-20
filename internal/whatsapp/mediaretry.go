package whatsapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waMmsRetry"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// WhatsApp's servers drop an attachment about 30 days after it was sent (its link's oe).
// The phone may still have it: a media retry receipt asks it to upload the file again,
// and its answer (events.MediaRetry) carries a new link, kept in place of the old one.

// retryWait is how long the phone has to upload an attachment again.
const retryWait = 30 * time.Second

// retryRest is how long an account's phone is not asked again after it let one go
// unanswered: an offline phone would otherwise hold every attachment for retryWait.
const retryRest = 10 * time.Minute

var (
	errPhoneQuiet  = errors.New("the phone did not answer lately; asked again later")
	errPhoneSilent = errors.New("the phone did not answer in time")
)

// retry is one attachment asked of the phone, and its answer once it came.
type retry struct {
	done   chan struct{}
	answer *events.MediaRetry
}

// reupload asks the phone to upload part again, waits for its answer, keeps the new link
// and downloads from it. A phone that no longer has it is ErrMediaGone.
func (a *Adapter) reupload(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, kind string, part whatsmeow.DownloadableMessage) ([]byte, error) {
	chat, id, sender, account, client, err := a.target(ctx, roomID, eventID)
	if err != nil {
		return nil, err
	}
	key := part.GetMediaKey()
	if len(key) == 0 {
		return nil, fmt.Errorf("whatsapp: the attachment of %s expired, with no key to ask the phone for it: %w", eventID, domain.ErrMediaGone)
	}
	r, first, err := a.awaiting(account.Digits, id)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: the attachment of %s expired; %w", eventID, err)
	}
	if first {
		info := &types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsFromMe: selfOf(client).is(sender), IsGroup: chat.Server == types.GroupServer},
			ID:            id,
		}
		if err = client.SendMediaRetryReceipt(ctx, info, key); err != nil {
			a.forget(id, r)
			return nil, fmt.Errorf("whatsapp: ask the phone for the attachment of %s again: %w", eventID, err)
		}
	}
	waiting, cancel := context.WithTimeout(ctx, retryWait)
	defer cancel()
	answer, err := a.answer(waiting, account.Digits, id, r)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: the attachment of %s expired; %w", eventID, err)
	}
	path, err := newPath(answer, key)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: the phone did not upload the attachment of %s again: %w", eventID, err)
	}
	if err := a.keepPath(ctx, roomID, eventID, kind, part, path); err != nil {
		return nil, err
	}
	return download(ctx, client, eventID, part)
}

// awaiting is the retry for message id: a new one (first, so the caller asks the
// phone) or the one already asked. A phone that let one go unanswered lately is not
// asked.
func (a *Adapter) awaiting(digits string, id types.MessageID) (*retry, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r, ok := a.retries[id]; ok {
		return r, false, nil
	}
	if time.Now().Before(a.quietUntil[digits]) {
		return nil, false, errPhoneQuiet
	}
	r := &retry{done: make(chan struct{})}
	a.retries[id] = r
	return r, true, nil
}

// answer waits for r's answer until ctx ends; one that runs out of time rests the
// account's phone for retryRest.
func (a *Adapter) answer(ctx context.Context, digits string, id types.MessageID, r *retry) (*events.MediaRetry, error) {
	select {
	case <-r.done:
		return r.answer, nil
	case <-ctx.Done():
	}
	a.forget(id, r)
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("waiting for the phone: %w", ctx.Err())
	}
	a.mu.Lock()
	a.quietUntil[digits] = time.Now().Add(retryRest)
	a.mu.Unlock()
	return nil, errPhoneSilent
}

// forget drops r unless an answer or a later ask replaced it.
func (a *Adapter) forget(id types.MessageID, r *retry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.retries[id] == r {
		delete(a.retries, id)
	}
}

// onMediaRetry hands the phone's answer to whoever asked; one nobody waits for any
// more is dropped (the next load asks again).
func (a *Adapter) onMediaRetry(evt any) {
	e, ok := evt.(*events.MediaRetry)
	if !ok {
		return
	}
	a.mu.Lock()
	r, ok := a.retries[e.MessageID]
	delete(a.retries, e.MessageID)
	a.mu.Unlock()
	if ok {
		r.answer = e
		close(r.done)
	}
}

// newPath is the direct path the phone's answer gives; one saying the phone no longer
// has the file is ErrMediaGone.
func newPath(answer *events.MediaRetry, key []byte) (string, error) {
	notice, err := whatsmeow.DecryptMediaRetryNotification(answer, key)
	switch {
	case errors.Is(err, whatsmeow.ErrMediaNotAvailableOnPhone):
		return "", fmt.Errorf("%w: %w", domain.ErrMediaGone, err)
	case err != nil:
		return "", fmt.Errorf("its answer: %w", err)
	case notice.GetResult() == waMmsRetry.MediaRetryNotification_NOT_FOUND:
		return "", fmt.Errorf("%w: the phone did not find it", domain.ErrMediaGone)
	case notice.GetResult() != waMmsRetry.MediaRetryNotification_SUCCESS || notice.GetDirectPath() == "":
		return "", fmt.Errorf("the phone answered %s", notice.GetResult())
	}
	return notice.GetDirectPath(), nil
}

// keepPath sets part's direct path to path and keeps it as the message's source.
func (a *Adapter) keepPath(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, kind string, part whatsmeow.DownloadableMessage, path string) error {
	msg, ok := part.(proto.Message)
	if !ok {
		return fmt.Errorf("whatsapp: the attachment of %s is not a message part", eventID)
	}
	fields := msg.ProtoReflect()
	fields.Set(fields.Descriptor().Fields().ByName("directPath"), protoreflect.ValueOfString(path))
	encoded, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("whatsapp: keep the new link of %s: %w", eventID, err)
	}
	raw, err := json.Marshal(mediaSource{Kind: kind, Proto: base64.StdEncoding.EncodeToString(encoded)})
	if err != nil {
		return fmt.Errorf("whatsapp: keep the new link of %s: %w", eventID, err)
	}
	if err := a.cache.SaveMediaSource(ctx, eventID, roomID, "", string(raw)); err != nil {
		return fmt.Errorf("whatsapp: keep the new link of %s: %w", eventID, err)
	}
	return nil
}
