package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waMmsRetry"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/gcmutil"
	"go.mau.fi/whatsmeow/util/hkdfutil"
	"google.golang.org/protobuf/proto"

	"github.com/EugeneShtoka/kith/internal/domain"
)

var retryKey = []byte("0123456789abcdef0123456789abcdef")

// phoneAnswers is the phone's answer to a media retry for message id, encrypted as
// WhatsApp does.
func phoneAnswers(t *testing.T, id types.MessageID, key []byte, result waMmsRetry.MediaRetryNotification_ResultType, path string) *events.MediaRetry {
	t.Helper()
	plain, err := proto.Marshal(&waMmsRetry.MediaRetryNotification{StanzaID: new(id), DirectPath: new(path), Result: &result})
	if err != nil {
		t.Fatal(err)
	}
	iv := []byte("twelve bytes")
	sealed, err := gcmutil.Encrypt(hkdfutil.SHA256(key, nil, []byte("WhatsApp Media Retry Notification"), 32), iv, plain, []byte(id))
	if err != nil {
		t.Fatal(err)
	}
	return &events.MediaRetry{MessageID: id, Ciphertext: sealed, IV: iv}
}

// Everyone loading an attachment while the phone is asked shares the one ask and its
// answer; one who stops waiting, or a late answer, leaves nothing behind; a phone that
// lets one go unanswered is not asked again for a while, other accounts' still are.
func TestThePhoneIsAskedOnceAndRestsWhenSilent(t *testing.T) {
	t.Parallel()
	account := Account{Name: "home", Digits: ownDigits}
	a, _, _ := offline(t, account)
	long, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r, first, err := a.awaiting(ownDigits, "M1")
	if err != nil || !first {
		t.Fatalf("the first ask = %v, %v", first, err)
	}
	var got [3]*events.MediaRetry
	var wg sync.WaitGroup
	for i := range got {
		mine, asks, err := a.awaiting(ownDigits, "M1")
		if err != nil || asks || mine != r {
			t.Fatalf("a second load asks the phone again: %v, %v", asks, err)
		}
		wg.Go(func() { got[i], _ = a.answer(long, ownDigits, "M1", mine) })
	}
	a.onMediaRetry(&events.MediaRetry{MessageID: "someone else's"})
	answer := phoneAnswers(t, "M1", retryKey, waMmsRetry.MediaRetryNotification_SUCCESS, "/v/new.enc")
	a.onMediaRetry(answer)
	wg.Wait()
	for i, g := range got {
		if g != answer {
			t.Errorf("waiter %d got %v", i, g)
		}
	}

	r, _, _ = a.awaiting(ownDigits, "M2")
	stopped, stop := context.WithCancel(context.Background())
	stop()
	if _, err := a.answer(stopped, ownDigits, "M2", r); !errors.Is(err, context.Canceled) {
		t.Errorf("a load that stopped waiting = %v", err)
	}
	if _, first, _ := a.awaiting(ownDigits, "M2"); !first {
		t.Error("a load that stopped waiting left the next one waiting on its ask")
	}
	a.onMediaRetry(phoneAnswers(t, "M2", retryKey, waMmsRetry.MediaRetryNotification_SUCCESS, "/v/late.enc"))
	if _, first, err := a.awaiting(ownDigits, "M2"); !first || err != nil {
		t.Errorf("after a load stopped waiting, the next asks again: %v, %v", first, err)
	}

	r, _, _ = a.awaiting(ownDigits, "M3")
	short, cancelShort := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancelShort()
	if _, err := a.answer(short, ownDigits, "M3", r); !errors.Is(err, errPhoneSilent) {
		t.Errorf("no answer in time = %v", err)
	}
	if _, _, err := a.awaiting(ownDigits, "M4"); !errors.Is(err, errPhoneQuiet) {
		t.Errorf("asking a phone that just let one go unanswered = %v", err)
	}
	if _, first, err := a.awaiting("44000000002", "M5"); !first || err != nil {
		t.Errorf("another account's phone: %v, %v", first, err)
	}
	if _, asks, err := a.awaiting(ownDigits, "M2"); asks || err != nil {
		t.Errorf("an ask already out is still waited for while the phone rests: %v, %v", asks, err)
	}
}

// The phone's answer is a new link, or says it no longer has the file (gone), or fails
// some other way (not gone: asked again later).
func TestThePhonesAnswerIsANewLinkOrGone(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		answer *events.MediaRetry
		path   string
		gone   bool
	}{
		{"uploaded again", phoneAnswers(t, "M", retryKey, waMmsRetry.MediaRetryNotification_SUCCESS, "/v/new.enc"), "/v/new.enc", false},
		{"not on the phone", &events.MediaRetry{MessageID: "M", Error: &events.MediaRetryError{Code: 2}}, "", true},
		{"not found", phoneAnswers(t, "M", retryKey, waMmsRetry.MediaRetryNotification_NOT_FOUND, ""), "", true},
		{"another error", &events.MediaRetry{MessageID: "M", Error: &events.MediaRetryError{Code: 1}}, "", false},
		{"a general error", phoneAnswers(t, "M", retryKey, waMmsRetry.MediaRetryNotification_GENERAL_ERROR, ""), "", false},
		{"success without a link", phoneAnswers(t, "M", retryKey, waMmsRetry.MediaRetryNotification_SUCCESS, ""), "", false},
		{"under another key", phoneAnswers(t, "M", []byte("another key, another attachment"), waMmsRetry.MediaRetryNotification_SUCCESS, "/v/x"), "", false},
	} {
		path, err := newPath(c.answer, retryKey)
		if path != c.path || errors.Is(err, domain.ErrMediaGone) != c.gone || (c.path == "") == (err == nil) {
			t.Errorf("%s: %q, %v", c.name, path, err)
		}
	}
}

// The new link replaces the kept one, and the attachment keeps its keys.
func TestTheNewLinkIsKept(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, cache, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	e := danaWrites("3EB0S", "")
	e.Message = &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: new("image/jpeg"), DirectPath: new("/v/1.enc?oe=5F5E1000"), MediaKey: retryKey}}
	a.onMessage(ctx, account, client, e)
	event := domain.EventID("whatsapp:" + ownDigits + "/3EB0S")
	_, raw, _, err := cache.MediaSource(ctx, danaChat, event)
	if err != nil {
		t.Fatal(err)
	}
	var source mediaSource
	if err = json.Unmarshal([]byte(raw), &source); err != nil {
		t.Fatal(err)
	}
	part, err := source.downloadable()
	if err != nil {
		t.Fatal(err)
	}
	if err = a.keepPath(ctx, danaChat, event, source.Kind, part, "/v/2.enc?oe=7FFFFFFF"); err != nil {
		t.Fatal(err)
	}
	_, raw, _, _ = cache.MediaSource(ctx, danaChat, event)
	var kept mediaSource
	if err = json.Unmarshal([]byte(raw), &kept); err != nil {
		t.Fatal(err)
	}
	again, err := kept.downloadable()
	if err != nil || kept.Kind != kindImage || again.GetDirectPath() != "/v/2.enc?oe=7FFFFFFF" || !bytes.Equal(again.GetMediaKey(), retryKey) {
		t.Errorf("kept %+v: %v", again, err)
	}
}
