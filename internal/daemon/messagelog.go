package daemon

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	pc "github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// MessageLog is the cache's numbering of message changes (db.Cache): where it stands,
// and each message changed after a number, as the cache holds it now.
type MessageLog interface {
	LastChange(ctx context.Context) (epoch string, last int64, err error)
	ChangesAfter(ctx context.Context, after int64, limit int) (epoch string, changes []domain.MessageChange, err error)
}

const (
	// tailBatch is how many changes one read of the cache sends.
	tailBatch = 256
	// tailBehind is how many numbers a resume may be behind before the client is told
	// to read again rather than sent the backlog.
	tailBehind = 5000
)

// Messages streams the cache's changes from since, or from the newest, and then each
// change as it is committed: every room, as the live stream always has (a client may
// have a room open from outside its profile). A reader that falls behind lags and
// catches up; nothing is dropped.
func (s *server) Messages(ctx context.Context, r *req[v1.MessagesRequest], st *connect.ServerStream[v1.MessagesResponse]) error {
	// Where it reads from is taken before the headers go: they tell the client the
	// stream is open, and it reads anything older itself.
	epoch, at, readAgain, err := s.resumeAt(ctx, r.Msg)
	if err != nil {
		return err
	}
	s.Streams.tail.join()
	defer s.Streams.tail.leave()
	// Send headers now: the client bounds its wait for them (answerTimeout), and a stream
	// can be silent for hours.
	if err := st.Send(nil); err != nil {
		return rpcErr(fmt.Errorf("open stream: %w", err))
	}
	if readAgain {
		if err := send(st, &v1.MessagesResponse{Epoch: epoch, Change: at, ReadAgain: true}); err != nil {
			return err
		}
	}
	for {
		bell, ended := s.Streams.tail.next()
		if ended {
			return nil
		}
		now, changes, err := s.MessageLog.ChangesAfter(ctx, at, tailBatch)
		if err != nil {
			return rpcErr(err)
		}
		if now != epoch {
			// Cleared: the numbers go on, but what the client holds is gone.
			epoch = now
			if err := send(st, &v1.MessagesResponse{Epoch: epoch, Change: at, ReadAgain: true}); err != nil {
				return err
			}
		}
		for i := range changes {
			c := &changes[i]
			if err := send(st, &v1.MessagesResponse{Message: pc.MessageToProto(c.Message), Epoch: epoch, Change: c.Number}); err != nil {
				return err
			}
			at = c.Number
		}
		if len(changes) == tailBatch {
			continue // more already numbered
		}
		select {
		case <-ctx.Done():
			return nil
		case <-bell:
		}
	}
}

// resumeAt is where a stream asking for since reads from: there, or, when the cache
// cannot resume from it (another epoch, a number it never gave, too far behind), the
// newest, the client to be told to read again.
func (s *server) resumeAt(ctx context.Context, since *v1.MessagesRequest) (epoch string, at int64, readAgain bool, err error) {
	epoch, last, err := s.MessageLog.LastChange(ctx)
	if err != nil {
		return "", 0, false, rpcErr(err)
	}
	if since.GetSinceEpoch() == "" {
		return epoch, last, false, nil
	}
	from := since.GetSinceChange()
	if since.GetSinceEpoch() == epoch && from <= last && last-from <= tailBehind {
		return epoch, from, false, nil
	}
	return epoch, last, true, nil
}

func send(st *connect.ServerStream[v1.MessagesResponse], frame *v1.MessagesResponse) error {
	if err := st.Send(frame); err != nil {
		return rpcErr(fmt.Errorf("send stream frame: %w", err))
	}
	return nil
}
