package daemon

import (
	"context"
	"errors"
	"slices"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// Notice is one notification kithd published, under an ID shared by every profile it
// was published to. Code is a verification code it announces, to copy.
type Notice struct {
	ID    string
	Alert notify.Notification
	Code  string
}

// NotificationStream streams the notifications published to the request's profile. The
// profile is read per notification, so a reload that renames or removes it reaches an
// open stream. It queues as much as the notifier itself does: a desktop slow to show
// a burst must not lose any of it.
func (s *server) NotificationStream(ctx context.Context, _ *req[v1.NotificationStreamRequest], st *connect.ServerStream[v1.NotificationStreamResponse]) error {
	return serveStreamWith(ctx, s.Notifications.notices, notifierBuffer, st, func(v notice) *v1.NotificationStreamResponse {
		if !slices.Contains(v.topics, seatKey(s.requestProfile(ctx))) {
			return nil
		}
		return &v1.NotificationStreamResponse{Notification: noticeToProto(v)}
	})
}

func noticeToProto(v notice) *v1.Notification {
	a := v.alert
	var sent int64
	if !a.Sent.IsZero() {
		sent = a.Sent.UnixMilli()
	}
	return &v1.Notification{
		Id: v.id, Sender: a.Sender, SenderId: a.SenderID, Room: a.Room, Space: a.Space,
		Body: a.Body, Protocol: a.Protocol, SentMs: sent, Date: a.Date, Time: a.Time,
		Sound: a.Sound, Silent: a.Silent, Replaces: a.Replaces, Code: v.code,
	}
}

func protoToNotice(p *v1.Notification) Notice {
	var sent time.Time
	if p.GetSentMs() != 0 {
		sent = time.UnixMilli(p.GetSentMs())
	}
	return Notice{ID: p.GetId(), Alert: notify.Notification{
		Sender: p.GetSender(), SenderID: p.GetSenderId(), Room: p.GetRoom(), Space: p.GetSpace(),
		Body: p.GetBody(), Protocol: p.GetProtocol(), Sent: sent, Date: p.GetDate(), Time: p.GetTime(),
		Sound: p.GetSound(), Silent: p.GetSilent(), Replaces: p.GetReplaces(),
	}, Code: p.GetCode()}
}

// Notifications listens to the notifications published to this client's profile
// (UseProfile), calling opened once the daemon has subscribed it and show for each,
// until the stream ends or ctx does. It always returns an error saying which.
func (r *Remote) Notifications(ctx context.Context, opened func(), show func(Notice)) error {
	st, err := r.c.NotificationStream(ctx, connect.NewRequest(&v1.NotificationStreamRequest{}))
	if err != nil {
		return callErr("subscribe to notifications", err)
	}
	defer func() { _ = st.Close() }()
	// The daemon answers once it has subscribed the stream; a call that never reached
	// it (kithd down) has no headers and is not open, nor is one a waiting daemon refused.
	if h := st.ResponseHeader(); h.Get("Content-Type") != "" && h.Get(sentinelHeader) == "" {
		opened()
	}
	for st.Receive() {
		if n := st.Msg().GetNotification(); n != nil {
			show(protoToNotice(n))
		}
	}
	if err := st.Err(); err != nil {
		return callErr("notification stream", err)
	}
	return callErr("notification stream", errors.New("the daemon ended it"))
}
