package telegram

import (
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgtest"

	"github.com/EugeneShtoka/kith/internal/api"
)

// A message Telegram no longer has (it answers an empty message for a deleted one) is
// no such message, in Telegram's words.
func TestAMessageTelegramNoLongerHasIsNoSuchMessage(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.serveUpdates(&updatesOf{pts: 1})
	f.cluster.Dispatch(2, "dc2").HandleFunc(tg.MessagesGetMessagesRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		return sendResult(s, r, &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.MessageEmpty{ID: 5}}})
	})
	st := openStore(t)
	knowDana(t, st)
	a, _ := loggedInWithStore(t, f, st)
	_, err := a.FetchEvent(t.Context(), "telegram:42/7", "telegram:42/7/5")
	if !errors.Is(err, api.ErrNoSuchMessage) || !strings.Contains(err.Error(), "telegram: the message is gone") {
		t.Errorf("FetchEvent() = %v, want ErrNoSuchMessage in Telegram's words", err)
	}
}
