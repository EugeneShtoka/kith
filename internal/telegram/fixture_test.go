package telegram

import (
	"context"
	"sync"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tgtest"
)

// The fake's replies are encoded one at a time. Encoding a Telegram object writes its
// flags field, and the fixtures in replies are shared: dana by tests running in
// parallel, a fake's own user by handlers answering at once. Encoded together, the
// same struct is written twice at once, which the race detector rightly calls a race.

// encodeMu is held while a reply is encoded.
var encodeMu sync.Mutex

// encoded is a reply already encoded, sent as its bytes.
type encoded struct {
	raw []byte
	err error
}

func (e encoded) Encode(b *bin.Buffer) error {
	if e.err != nil {
		return e.err
	}
	b.Put(e.raw)
	return nil
}

// frozen is msg encoded now, under encodeMu.
func frozen(msg bin.Encoder) bin.Encoder {
	encodeMu.Lock()
	defer encodeMu.Unlock()
	var b bin.Buffer
	if err := msg.Encode(&b); err != nil {
		return encoded{err: err}
	}
	return encoded{raw: b.Copy()}
}

// sendResult is s.SendResult with msg encoded under encodeMu.
func sendResult(s *tgtest.Server, req *tgtest.Request, msg bin.Encoder) error {
	return s.SendResult(req, frozen(msg))
}

// send is s.Send with msg encoded under encodeMu.
func send(ctx context.Context, s *tgtest.Server, k tgtest.Session, t proto.MessageType, msg bin.Encoder) error {
	return s.Send(ctx, k, t, frozen(msg))
}
