package slack

import (
	"context"
	"time"

	slackgo "github.com/slack-go/slack"
)

// A workspace's live events come over the web client's websocket (RTM), on the same
// session: messages as they are posted, and conversations joined, left or renamed.

// drainFor bounds how long a let-go connection's last events are read, so its
// websocket can close (it blocks on a full channel otherwise).
const drainFor = 10 * time.Second

// goLive follows a connected workspace's live events until it is let go or the
// adapter's run ends.
func (a *Adapter) goLive(w *workspace) {
	a.mu.Lock()
	ctx := a.run
	a.mu.Unlock()
	if ctx == nil {
		ctx = context.Background() // signed in before Start: Stop lets it go
	}
	go a.live(ctx, w)
}

// live is one workspace's websocket, reconnected by slack-go as it drops.
func (a *Adapter) live(ctx context.Context, w *workspace) {
	rtm := w.client.NewRTM()
	go rtm.ManageConnection()
	defer a.letGo(rtm)
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.done:
			return
		case ev := <-rtm.IncomingEvents:
			if !a.onEvent(ctx, w, ev) {
				return
			}
		}
	}
}

// letGo closes a websocket, reading what it still sends until it says it is closed.
func (a *Adapter) letGo(rtm *slackgo.RTM) {
	_ = rtm.Disconnect() // already disconnected is as good
	deadline := time.After(drainFor)
	for {
		select {
		case ev := <-rtm.IncomingEvents:
			if d, ok := ev.Data.(*slackgo.DisconnectedEvent); ok && d.Intentional {
				return
			}
		case <-deadline:
			return
		}
	}
}

// onEvent handles one live event; false when the connection is over.
func (a *Adapter) onEvent(ctx context.Context, w *workspace, ev slackgo.RTMEvent) bool {
	switch e := ev.Data.(type) {
	case *slackgo.MessageEvent:
		a.arrived(ctx, w, e.Channel, &e.Msg)
	case *slackgo.ConnectedEvent:
		if a.current(w) {
			a.session(w.account, Connected, "")
		}
	case *slackgo.ConnectionErrorEvent:
		a.log.Warn("Slack's websocket dropped", "account", w.account.Name, "err", e.ErrorObj)
		if a.current(w) {
			a.session(w.account, Connecting, "Slack cannot be reached: "+e.Error())
		}
	case *slackgo.InvalidAuthEvent:
		if a.release(w) {
			a.session(w.account, SignedOut, "Slack ended the session; run `kith login slack "+w.account.Name+"` again")
		}
		return false
	case *slackgo.FatalConnectionErrorEvent:
		// slack-go gave up reconnecting: start over, from asking Slack who this is.
		if a.release(w) {
			go a.connect(ctx, w.account, w.creds)
		}
		return false
	case *slackgo.ChannelJoinedEvent, *slackgo.ChannelLeftEvent, *slackgo.ChannelRenameEvent,
		*slackgo.GroupJoinedEvent, *slackgo.GroupLeftEvent, *slackgo.GroupRenameEvent,
		*slackgo.IMCreatedEvent, *slackgo.ChannelArchiveEvent, *slackgo.GroupArchiveEvent:
		go a.relist(context.WithoutCancel(ctx), w)
	}
	return true
}

// current reports whether w is still its account's connection.
func (a *Adapter) current(w *workspace) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workspaces[w.account.Name] == w
}

// release lets w go when it is still its account's connection, and reports whether
// it was.
func (a *Adapter) release(w *workspace) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.workspaces[w.account.Name] != w {
		return false
	}
	delete(a.workspaces, w.account.Name)
	w.close()
	return true
}
