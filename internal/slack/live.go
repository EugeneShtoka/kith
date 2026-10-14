package slack

import (
	"context"
	"time"

	slackgo "github.com/slack-go/slack"
)

// Live events come over the web client's websocket (RTM) on the same session.

// drainFor bounds draining a closed connection's last events; its websocket blocks on a
// full channel otherwise.
const drainFor = 10 * time.Second

// goLive follows a workspace's live events until it is let go or the run ends.
func (a *Adapter) goLive(w *workspace) {
	a.mu.Lock()
	ctx := a.run
	a.mu.Unlock()
	if ctx == nil {
		ctx = context.Background() // signed in before Start: Stop lets it go
	}
	go a.live(ctx, w)
}

// live runs one workspace's websocket; slack-go reconnects it when it drops.
func (a *Adapter) live(ctx context.Context, w *workspace) {
	rtm := w.client.NewRTM()
	w.mu.Lock()
	w.rtm = rtm
	w.mu.Unlock()
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

// letGo closes a websocket, draining it until it reports closed.
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
		switch e.SubType {
		case "message_changed":
			a.onEdited(ctx, w, e)
		case "message_deleted":
			a.onDeleted(ctx, w, e)
		default:
			a.arrived(ctx, w, e.Channel, &e.Msg)
		}
	case *slackgo.ReactionAddedEvent:
		a.onReaction(ctx, w, slackgo.ReactionEvent(*e), true)
	case *slackgo.ReactionRemovedEvent:
		a.onReaction(ctx, w, slackgo.ReactionEvent(*e), false)
	case *slackgo.ChannelMarkedEvent, *slackgo.IMMarkedEvent, *slackgo.GroupMarkedEvent:
		a.onAnyMarked(ctx, w, e)
	case *slackgo.UserTypingEvent:
		a.onTyping(w, e)
	case *slackgo.ConnectedEvent:
		if a.current(w) {
			a.session(w.account, Connected, "")
			a.goCatchUp(ctx, w) // what was said while not connected
			go a.sendOwed(context.WithoutCancel(ctx), w)
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
		// slack-go gave up reconnecting: start over from auth.test.
		if a.release(w) {
			go a.connect(ctx, w.account, w.creds)
		}
		return false
	case *slackgo.ChannelJoinedEvent, *slackgo.ChannelLeftEvent, *slackgo.ChannelRenameEvent,
		*slackgo.GroupJoinedEvent, *slackgo.GroupLeftEvent, *slackgo.GroupRenameEvent,
		*slackgo.IMCreatedEvent, *slackgo.ChannelArchiveEvent, *slackgo.GroupArchiveEvent:
		go a.relist(context.WithoutCancel(ctx), w)
	case *slackgo.UnmarshallingErrorEvent:
		a.onUnmapped(ctx, w, e.Raw)
	}
	return true
}

// onAnyMarked moves the read position of a channel, DM or group DM that was read.
func (a *Adapter) onAnyMarked(ctx context.Context, w *workspace, e any) {
	switch e := e.(type) {
	case *slackgo.ChannelMarkedEvent:
		a.onMarked(ctx, w, e.Channel, e.Timestamp)
	case *slackgo.IMMarkedEvent:
		a.onMarked(ctx, w, e.Channel, e.Timestamp)
	case *slackgo.GroupMarkedEvent:
		a.onMarked(ctx, w, e.Channel, e.Timestamp)
	}
}

// current reports whether w is still its account's connection.
func (a *Adapter) current(w *workspace) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workspaces[w.account.Name] == w
}

// release lets w go if it is still its account's connection, reporting whether it was.
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
