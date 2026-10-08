package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // register GIF decoder for inline media
	_ "image/jpeg" // register JPEG decoder
	_ "image/png"  // register PNG decoder
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	_ "golang.org/x/image/webp" // register WebP: the format every bridged sticker arrives in

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/focus"
	"github.com/EugeneShtoka/kith/internal/media"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// errNoDaemon is returned when something only the daemon can do is asked of a model
// without one (as in most tests).
var errNoDaemon = errors.New("not attached to kithd")

// roomsMsg carries the result of loading the joined room list.
type roomsMsg struct {
	rooms []domain.Room
	err   error
}

// spacesMsg carries the result of loading the joined space hierarchy.
type spacesMsg struct {
	spaces []domain.Space
	err    error
}

// incomingMsg carries one message streamed from the sync loop.
type incomingMsg struct{ message domain.Message }

// timelineMsg carries a fetched page of scrollback. roomID guards against a page
// arriving after the user switched rooms.
type timelineMsg struct {
	roomID domain.RoomID
	page   domain.TimelinePage
	err    error
}

// cachedTimelineMsg carries a room's cached messages for instant display on entry.
type cachedTimelineMsg struct {
	roomID   domain.RoomID
	messages []domain.Message
	err      error
}

// unreadMsg carries every room's cached unread state, loaded once at startup.
type unreadMsg struct {
	list []domain.Unread
	err  error
}

// unreadUpdateMsg carries one room's unread state, streamed from the sync loop.
type unreadUpdateMsg struct{ u domain.Unread }

// invitesMsg carries the cached invitation set at startup; inviteUpdateMsg carries
// one streamed from sync. Both are the whole set, never a delta; only the streamed
// one re-subscribes.
type invitesMsg struct {
	invites []domain.Room
	err     error
}

type inviteUpdateMsg struct{ invites []domain.Room }

// joinedMsg and leftMsg report the outcome of a membership change.
type joinedMsg struct {
	roomID domain.RoomID
	err    error
}

type leftMsg struct {
	roomID domain.RoomID
	err    error
}

// membersMsg carries a room's ranked mention candidates.
type membersMsg struct {
	roomID  domain.RoomID
	members []domain.Member
	err     error
}

// searchResultsMsg carries the hits for one search, with the query and scope it was
// run for so stale results are dropped.
type searchResultsMsg struct {
	query string
	scope searchScope
	hits  []domain.SearchHit
	err   error
}

// cachedReactionsMsg carries a room's cached reactions.
type cachedReactionsMsg struct {
	roomID    domain.RoomID
	reactions []domain.Reaction
	err       error
}

// reactionUpdateMsg carries one reaction add or removal from sync.
type reactionUpdateMsg struct{ u domain.ReactionUpdate }

// imageLoadedMsg carries an inline image's rendered rows (or an error).
type imageLoadedMsg struct {
	eventID domain.EventID
	rows    []string
	err     error
}

// verifyMsg carries one interactive device-verification step from the backend.
type verifyMsg struct{ v domain.Verification }

// sentMsg reports the outcome of a send. It carries the draft because after a
// failure it is the only remaining copy of the words (see Model.handleSent).
type sentMsg struct {
	err    error
	roomID domain.RoomID
	draft  domain.Draft
}

// reactionSentMsg is the outcome of posting a reaction. The key is kept because a
// failure is how we learn a network refuses that emoji.
type reactionSentMsg struct {
	key string
	err error
}

// syncEndedMsg reports that the /sync loop returned (normally on shutdown).
type syncEndedMsg struct{ err error }

// attachedMsg reports the daemon connection dropping (false) or coming back
// (true). Only the daemon-backed client raises it; see api.Backend.Attached.
type attachedMsg struct{ attached bool }

// loadRoomsCmd reads the cached room list; refreshRoomsCmd fetches it from the
// homeserver. Both land as roomsMsg.
func (m Model) loadRoomsCmd() tea.Cmd { return fetch(m.ctx, m.backend.Rooms, wrapRooms) }

func (m Model) refreshRoomsCmd() tea.Cmd { return fetch(m.ctx, m.backend.RefreshRooms, wrapRooms) }

// wrapRooms and wrapSpaces are shared by the cached and the refreshed load.
func wrapRooms(rooms []domain.Room, err error) tea.Msg { return roomsMsg{rooms: rooms, err: err} }

func (m Model) loadSpacesCmd() tea.Cmd { return fetch(m.ctx, m.backend.Spaces, wrapSpaces) }

func (m Model) refreshSpacesCmd() tea.Cmd { return fetch(m.ctx, m.backend.RefreshSpaces, wrapSpaces) }

func wrapSpaces(spaces []domain.Space, err error) tea.Msg { return spacesMsg{spaces: spaces, err: err} }

func (m Model) startSyncCmd() tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg { return syncEndedMsg{err: backend.Start(ctx)} }
}

// fetch runs one backend call off the event loop and wraps its outcome as a message.
// ctx is an argument, not a capture, so it is read on the event loop; call and wrap
// run on the command's goroutine and must not read the model (see cmdreach_test.go).
func fetch[T any](ctx context.Context, call func(context.Context) (T, error), wrap func(T, error) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		v, err := call(ctx)
		return wrap(v, err)
	}
}

// fire runs one backend write off the event loop without reporting back: for writes
// whose failure costs nothing worth saying on screen. A failure is still logged, at
// warn, under op.
func fire(ctx context.Context, log *slog.Logger, op string, call func(context.Context) error) tea.Cmd {
	return func() tea.Msg {
		if err := call(ctx); err != nil && log != nil {
			level := slog.LevelWarn
			if ctx.Err() != nil {
				level = slog.LevelDebug // the program ending cancels what is in flight (tui.Run)
			}
			log.Log(ctx, level, op+" failed", "op", op, "err", err)
		}
		return nil
	}
}

// listen waits for one value from a daemon stream and wraps it as a message. A closed
// channel or canceled context returns nil so the listener ends instead of re-arming.
func listen[T any](ctx context.Context, ch <-chan T, wrap func(T) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		select {
		case v, ok := <-ch:
			if !ok {
				return nil
			}
			return wrap(v)
		case <-ctx.Done():
			return nil
		}
	}
}

func (m Model) listenCmd() tea.Cmd {
	return listen(m.ctx, m.backend.Messages(),
		func(msg domain.Message) tea.Msg { return incomingMsg{message: msg} })
}

// cachedTimelineCmd reads a room's cached messages; loadTimelineCmd fetches a live
// page. Both are merged into the same timeline.
func (m Model) cachedTimelineCmd(roomID domain.RoomID) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		msgs, err := backend.CachedTimeline(ctx, roomID)
		return cachedTimelineMsg{roomID: roomID, messages: msgs, err: err}
	}
}

func (m Model) loadTimelineCmd(roomID domain.RoomID, from string) tea.Cmd {
	if roomID == "" {
		return nil // no room open (an empty list): nothing to ask about
	}
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		page, err := backend.Timeline(ctx, roomID, from, timelinePageSize)
		return timelineMsg{roomID: roomID, page: page, err: err}
	}
}

// loadUnreadCmd reads the cached per-room unread state.
func (m Model) loadUnreadCmd() tea.Cmd {
	return fetch(m.ctx, m.backend.CachedUnread,
		func(list []domain.Unread, err error) tea.Msg { return unreadMsg{list: list, err: err} })
}

// listenAttachedCmd waits for the daemon connection to drop or come back.
func (m Model) listenAttachedCmd() tea.Cmd {
	return listen(m.ctx, m.backend.Attached(),
		func(attached bool) tea.Msg { return attachedMsg{attached: attached} })
}

func (m Model) listenUnreadCmd() tea.Cmd {
	return listen(m.ctx, m.backend.Unread(),
		func(u domain.Unread) tea.Msg { return unreadUpdateMsg{u: u} })
}

// loadInvitesCmd reads the cached invitations; listenInvitesCmd follows the stream.
func (m Model) loadInvitesCmd() tea.Cmd {
	return fetch(m.ctx, m.backend.CachedInvites,
		func(invites []domain.Room, err error) tea.Msg { return invitesMsg{invites: invites, err: err} })
}

func (m Model) listenInvitesCmd() tea.Cmd {
	return listen(m.ctx, m.backend.Invites(),
		func(invites []domain.Room) tea.Msg { return inviteUpdateMsg{invites: invites} })
}

// searchCmd searches the local cache; cheap enough to run on every keystroke.
func (m Model) searchCmd(query string, req domain.SearchRequest) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	scope := m.search.scope
	return func() tea.Msg {
		hits, err := backend.SearchMessages(ctx, req)
		return searchResultsMsg{query: query, scope: scope, hits: hits, err: err}
	}
}

// searchSendersMsg carries the people a search can be narrowed to, tagged with its
// scope so a stale answer is dropped.
type searchSendersMsg struct {
	scope   searchScope
	senders []domain.Member
	err     error
}

// searchSendersCmd asks who has posted in the search's rooms, once per scope.
func (m Model) searchSendersCmd(rooms domain.RoomSet, scope searchScope) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		senders, err := backend.SearchSenders(ctx, rooms, searchSenderLimit)
		return searchSendersMsg{scope: scope, senders: senders, err: err}
	}
}

// directCandidatesMsg carries the people this account could start a DM with. It
// arrives after the switcher opens; its rows sort last.
type directCandidatesMsg struct {
	people []domain.Member
	err    error
}

// directCandidateLimit caps the people offered to start a DM with.
const directCandidateLimit = 200

// directCandidatesCmd asks who this account has no DM with, when the switcher opens.
func (m Model) directCandidatesCmd() tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		people, err := backend.DirectCandidates(ctx, directCandidateLimit)
		return directCandidatesMsg{people: people, err: err}
	}
}

// lastMessagesMsg carries when each room last had a message.
type lastMessagesMsg struct {
	at  map[domain.RoomID]time.Time
	err error
}

// lastMessagesCmd asks the cache once at startup; the message stream keeps it current.
func (m Model) lastMessagesCmd() tea.Cmd {
	return fetch(m.ctx, m.backend.LastMessages,
		func(at map[domain.RoomID]time.Time, err error) tea.Msg { return lastMessagesMsg{at: at, err: err} })
}

// redactedMsg reports a deletion attempt; the room lets a failure restore the row.
type redactedMsg struct {
	roomID domain.RoomID
	err    error
}

// redactCmd asks the daemon to delete a message; the change arrives on the stream.
func (m Model) redactCmd(roomID domain.RoomID, eventID domain.EventID) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		return redactedMsg{roomID: roomID, err: backend.Redact(ctx, roomID, eventID, "")}
	}
}

// starredMsg reports a star attempt. Only a failure matters: the set returns via sync.
type starredMsg struct {
	roomID  domain.RoomID
	eventID domain.EventID
	on      bool
	err     error
}

// starCmd asks the daemon to write the bookmark.
func (m Model) starCmd(roomID domain.RoomID, eventID domain.EventID, on bool) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		return starredMsg{
			roomID: roomID, eventID: eventID, on: on,
			err: backend.StarMessage(ctx, roomID, eventID, on),
		}
	}
}

// rerunSearchCmd re-asks the current results question after an edit inside the list.
func (m Model) rerunSearchCmd() tea.Cmd {
	return func() tea.Msg { return rerunSearchMsg{} }
}

// rerunSearchMsg asks the model to re-run the open search after the preceding write.
type rerunSearchMsg struct{}

// editedMsg reports a revision attempt; the room lets a failure restore the old text.
type editedMsg struct {
	roomID domain.RoomID
	err    error
}

// editCmd sends a revision of a message already sent.
func (m Model) editCmd(roomID domain.RoomID, revision domain.Draft) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		err := backend.Send(ctx, roomID, revision)
		return editedMsg{roomID: roomID, err: err}
	}
}

// roomLoadMsg fires when the cursor has rested on a room long enough to load it;
// armed identifies the timer so a stale one is dropped.
type roomLoadMsg struct {
	roomID domain.RoomID
	armed  int
}

// joinRoomCmd joins a room by ID or alias (also accepting an invitation);
// leaveRoomCmd leaves one (also rejecting an invitation).
func (m Model) joinRoomCmd(target string, via ...string) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		roomID, err := backend.JoinRoom(ctx, target, via)
		return joinedMsg{roomID: roomID, err: err}
	}
}

func (m Model) leaveRoomCmd(roomID domain.RoomID) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		return leftMsg{roomID: roomID, err: backend.LeaveRoom(ctx, roomID)}
	}
}

// cachedReactionsCmd reads a room's cached reactions; listenReactionsCmd follows
// the stream.
func (m Model) cachedReactionsCmd(roomID domain.RoomID) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		rs, err := backend.CachedReactions(ctx, roomID)
		return cachedReactionsMsg{roomID: roomID, reactions: rs, err: err}
	}
}

func (m Model) listenReactionsCmd() tea.Cmd {
	return listen(m.ctx, m.backend.Reactions(),
		func(u domain.ReactionUpdate) tea.Msg { return reactionUpdateMsg{u: u} })
}

// activityMsg carries one room's typing set and moved read receipts.
type activityMsg struct{ a domain.Activity }

// listenActivityCmd waits for the next typing/receipt change in any room.
func (m Model) listenActivityCmd() tea.Cmd {
	return listen(m.ctx, m.backend.Activity(),
		func(a domain.Activity) tea.Msg { return activityMsg{a: a} })
}

// sendTypingCmd tells a room we started or stopped typing; a failure is only logged.
func (m Model) sendTypingCmd(roomID domain.RoomID, typing bool) tea.Cmd {
	backend := m.backend
	return fire(m.ctx, m.log, "send typing", func(ctx context.Context) error {
		return backend.SendTyping(ctx, roomID, typing, typingTimeout)
	})
}

// mediaJob is what a picture load needs from the event loop.
type mediaJob struct {
	roomID     domain.RoomID
	eventID    domain.EventID
	name, mime string
	// cache says whether this room's attachments may be written to disk.
	cache bool
	// deleted is a deleted message's attachment: kept apart (media.Cache.SetAside).
	deleted bool
}

// jobFor is a message's attachment as a fetch, viewing or playing it.
func (m Model) jobFor(msg domain.Message) mediaJob {
	return mediaJob{
		roomID: msg.RoomID, eventID: msg.ID,
		name: msg.Media.Name, mime: msg.Media.Mime,
		cache: m.mediaPolicy(msg).Cache, deleted: msg.Redacted,
	}
}

// loadImageCmd fetches, decodes and draws a message's image off the event loop,
// reusing a cached drawing for this cell box or the cached bytes when present. A
// failure returns empty rows so the load isn't retried.
func (m Model) loadImageCmd(job mediaJob, targetW, maxH int) tea.Cmd {
	ctx, backend, log := m.ctx, m.backend, m.log
	cache, blocks := m.pics.cache, m.pics.blocks
	return func() tea.Msg {
		loaded := imageLoadedMsg{eventID: job.eventID}

		if drawn, ok := cache.Render(job.eventID, targetW, maxH); ok {
			loaded.rows = strings.Split(string(drawn), "\n")
			return loaded
		}

		data, err := attachmentBytes(ctx, backend, log, cache, job)
		if err != nil {
			loaded.err = err
			return loaded
		}
		if len(data) == 0 {
			return loaded
		}

		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			loaded.err = err
			return loaded
		}
		loaded.rows = blockImage(blocks, img, targetW, maxH)
		if job.cache && len(loaded.rows) > 0 {
			// Best effort: a render not cached is drawn again next time.
			if perr := cache.PutRender(job.eventID, targetW, maxH, []byte(strings.Join(loaded.rows, "\n"))); perr != nil {
				log.Debug("cache picture render failed", "event", job.eventID, "err", perr)
			}
		}
		return loaded
	}
}

// attachmentBytes reads a message's attachment from the cache, else the backend,
// caching it unless the room says not to.
func attachmentBytes(ctx context.Context, backend api.Backend, log *slog.Logger, cache *media.Cache, job mediaJob) ([]byte, error) {
	if data, ok := cache.Read(job.eventID, job.name, job.mime); ok {
		return data, nil
	}
	data, err := backend.LoadImage(ctx, job.roomID, job.eventID)
	if err != nil {
		return nil, fmt.Errorf("load attachment: %w", err)
	}
	if len(data) > 0 && job.cache {
		// Cache write failures only cost speed.
		if _, werr := cache.Write(job.eventID, job.name, job.mime, data); werr != nil {
			log.Debug("cache attachment failed", "event", job.eventID, "err", werr)
		}
	}
	return data, nil
}

// fetchedEventMsg carries one event fetched by ID (an old thread root).
type fetchedEventMsg struct {
	roomID  domain.RoomID
	message domain.Message
	err     error
}

// fetchedQuoteMsg carries a reply's target fetched because it is not loaded. Separate
// from fetchedEventMsg: quotes are kept aside (quotes.go), not folded into the timeline.
type fetchedQuoteMsg struct {
	roomID  domain.RoomID
	message domain.Message
	err     error
}

// fetchQuoteCmd asks for one reply target.
func (m Model) fetchQuoteCmd(roomID domain.RoomID, eventID domain.EventID) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		msg, err := backend.FetchEvent(ctx, roomID, eventID)
		return fetchedQuoteMsg{roomID: roomID, message: msg, err: err}
	}
}

// fetchEventCmd asks the daemon for one event missing from the room's cached history.
func (m Model) fetchEventCmd(roomID domain.RoomID, eventID domain.EventID) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		msg, err := backend.FetchEvent(ctx, roomID, eventID)
		return fetchedEventMsg{roomID: roomID, message: msg, err: err}
	}
}

// sendCmd posts a draft, including its mentions and reply target.
func (m Model) sendCmd(roomID domain.RoomID, draft domain.Draft) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		return sentMsg{err: backend.Send(ctx, roomID, draft), roomID: roomID, draft: draft}
	}
}

// membersCmd loads a room's cached mention candidates; refreshMembersCmd refreshes
// the membership from the homeserver and re-ranks.
func (m Model) membersCmd(roomID domain.RoomID) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		members, err := backend.MentionCandidates(ctx, roomID, mentionCandidateLimit)
		return membersMsg{roomID: roomID, members: members, err: err}
	}
}

func (m Model) refreshMembersCmd(roomID domain.RoomID) tea.Cmd {
	if roomID == "" {
		return nil
	}
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		if _, err := backend.RefreshMembers(ctx, roomID); err != nil {
			return membersMsg{roomID: roomID, err: err}
		}
		members, err := backend.MentionCandidates(ctx, roomID, mentionCandidateLimit)
		return membersMsg{roomID: roomID, members: members, err: err}
	}
}

// roomCreatedMsg is the outcome of creating a room or a space. Both an ID and an
// error can be set: the room exists but filing it into a space failed.
type roomCreatedMsg struct {
	// enter asks to open the room once a refresh lists it.
	enter  bool
	roomID domain.RoomID
	name   string
	err    error
}

// createRoomCmd makes a room or a space.
func (m Model) createRoomCmd(spec domain.NewRoom) tea.Cmd {
	return m.createRoomAsCmd(spec, spec.Name, false)
}

// createRoomAsCmd is createRoomCmd with a label to report (a DM has no name) and an
// option to open the room once it exists.
func (m Model) createRoomAsCmd(spec domain.NewRoom, label string, enter bool) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		roomID, err := backend.CreateRoom(ctx, spec)
		return roomCreatedMsg{roomID: roomID, name: label, enter: enter, err: err}
	}
}

// memberAction is which of the four membership calls a command carries.
type memberAction int

const (
	memberInvite memberAction = iota
	memberKick
	memberBan
	memberUnban
)

// past is what the action reads as once it has happened.
func (a memberAction) past() string {
	return [...]string{"invited", "removed", "banned", "unbanned"}[a]
}

// present is what it reads as when it could not be done.
func (a memberAction) present() string {
	return [...]string{"invite", "remove", "ban", "unban"}[a]
}

// memberChangedMsg is the outcome of one membership change.
type memberChangedMsg struct {
	person string
	action memberAction
	err    error
}

// memberCmd carries out one membership change; the daemon does the power check.
func (m Model) memberCmd(roomID domain.RoomID, mxid string, act memberAction, reason string) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		var err error
		switch act {
		case memberInvite:
			err = backend.InviteUser(ctx, roomID, mxid)
		case memberKick:
			err = backend.KickUser(ctx, roomID, mxid, reason)
		case memberBan:
			err = backend.BanUser(ctx, roomID, mxid, reason)
		case memberUnban:
			err = backend.UnbanUser(ctx, roomID, mxid)
		}
		return memberChangedMsg{person: mxid, action: act, err: err}
	}
}

// spaceChange is one write the space picker decided on.
type spaceChange struct {
	id   domain.SpaceID
	name string
	add  bool
}

// spaceFiledMsg is the outcome of applying a set of spaceChanges, by space name.
type spaceFiledMsg struct {
	room string
	// kept: the space took the change but the room's parent event was refused
	// (api.ErrNoSpaceParent).
	added   []string
	removed []string
	kept    []string
	failed  []string
}

// landed is how many changes took, which decides whether to re-fetch the hierarchy.
func (msg spaceFiledMsg) landed() int { return len(msg.added) + len(msg.removed) + len(msg.kept) }

// summary is one line for the whole gesture, however many spaces it touched.
func (msg spaceFiledMsg) summary() string {
	var parts []string
	if len(msg.added) > 0 {
		parts = append(parts, "filed into "+strings.Join(msg.added, ", "))
	}
	if len(msg.removed) > 0 {
		parts = append(parts, "taken out of "+strings.Join(msg.removed, ", "))
	}
	if len(msg.kept) > 0 {
		parts = append(parts, "moved in "+strings.Join(msg.kept, ", ")+" but kept its own record of where it lives (no power there)")
	}
	if len(msg.failed) > 0 {
		parts = append(parts, "refused by "+strings.Join(msg.failed, ", "))
	}
	if len(parts) == 0 {
		return "nothing changed"
	}
	return msg.room + ": " + strings.Join(parts, " · ")
}

// fileRoomCmd applies every change the picker decided on, in order, and reports once.
// A refusal is collected rather than fatal.
func (m Model) fileRoomCmd(roomID domain.RoomID, room string, changes []spaceChange) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		out := spaceFiledMsg{room: room}
		for _, change := range changes {
			var err error
			if change.add {
				err = backend.AddToSpace(ctx, change.id, roomID)
			} else {
				err = backend.RemoveFromSpace(ctx, change.id, roomID)
			}
			switch {
			case err == nil && change.add:
				out.added = append(out.added, change.name)
			case err == nil:
				out.removed = append(out.removed, change.name)
			case errors.Is(err, api.ErrNoSpaceParent):
				out.kept = append(out.kept, change.name)
			default:
				out.failed = append(out.failed, change.name)
			}
		}
		return out
	}
}

// historyMsg carries one message's versions and its deletion, if any.
type historyMsg struct {
	eventID   domain.EventID
	revisions []domain.Revision
	deletion  domain.Deletion
	err       error
}

// messageHistoryCmd asks for every version of one message.
func (m Model) messageHistoryCmd(roomID domain.RoomID, eventID domain.EventID) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		revs, deletion, err := backend.MessageHistory(ctx, roomID, eventID)
		return historyMsg{eventID: eventID, revisions: revs, deletion: deletion, err: err}
	}
}

// senderSlotsMsg carries a room's remembered color slots back from the cache.
type senderSlotsMsg struct {
	roomID domain.RoomID
	slots  map[string]int
	err    error
}

// senderSlotsCmd asks the daemon what colors this room has been remembered with.
func (m Model) senderSlotsCmd(roomID domain.RoomID) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		slots, err := backend.SenderSlots(ctx, roomID)
		return senderSlotsMsg{roomID: roomID, slots: slots, err: err}
	}
}

// saveSenderSlotsCmd writes newly assigned colors back; failures only mean they are
// re-derived next time.
func (m Model) saveSenderSlotsCmd(roomID domain.RoomID, slots map[string]int) tea.Cmd {
	backend := m.backend
	return fire(m.ctx, m.log, "save sender colors", func(ctx context.Context) error { return backend.SaveSenderSlots(ctx, roomID, slots) })
}

// sendReactionCmd posts a reaction; it echoes back over the reaction stream, so only
// the outcome is routed back.
func (m Model) sendReactionCmd(roomID domain.RoomID, target domain.EventID, key string) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		return reactionSentMsg{key: key, err: backend.SendReaction(ctx, roomID, target, key)}
	}
}

// receiptSentMsg is the outcome of one read receipt for the open room (root is the
// thread's, empty for the main timeline). See handleReceiptSent.
type receiptSentMsg struct {
	roomID domain.RoomID
	root   domain.EventID
	event  domain.EventID
	err    error
}

// markReadCmd sends a read receipt; the outcome is routed back so a failure is said
// and retried on the next trigger.
func (m Model) markReadCmd(roomID domain.RoomID, eventID domain.EventID, private bool) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		err := backend.MarkRead(ctx, roomID, eventID, private)
		return receiptSentMsg{roomID: roomID, event: eventID, err: err}
	}
}

// threadPageMsg carries one page of a thread's own history, with the thread it was
// asked about so a page arriving after the view moved on can be dropped.
type threadPageMsg struct {
	roomID domain.RoomID
	root   domain.EventID
	page   domain.TimelinePage
	err    error
}

// threadPageCmd asks for the thread replies before the oldest one loaded.
func (m Model) threadPageCmd(roomID domain.RoomID, root domain.EventID, from string) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		page, err := backend.ThreadPage(ctx, roomID, root, from, timelinePageSize)
		return threadPageMsg{roomID: roomID, root: root, page: page, err: err}
	}
}

// roomThreadsMsg carries one room's full thread list ([display.threads]
// in_room_list = "all").
type roomThreadsMsg struct {
	roomID  domain.RoomID
	threads []domain.Thread
	err     error
}

// roomThreadsCmd asks the daemon for every thread in a room.
func (m Model) roomThreadsCmd(roomID domain.RoomID) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		threads, err := backend.ListThreads(ctx, roomID)
		return roomThreadsMsg{roomID: roomID, threads: threads, err: err}
	}
}

// markThreadReadCmd sends a threaded read receipt; routed back like markReadCmd.
func (m Model) markThreadReadCmd(roomID domain.RoomID, root, eventID domain.EventID, private bool) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		err := backend.MarkThreadRead(ctx, roomID, root, eventID, private)
		return receiptSentMsg{roomID: roomID, root: root, event: eventID, err: err}
	}
}

// markedReadMsg is the outcome of marking rooms read without opening them, beside
// the label and number of rooms asked for.
type markedReadMsg struct {
	label  string
	rooms  int
	result domain.ReadResult
	err    error
}

// markRoomsReadCmd marks whole rooms read. The answer is routed back since it was an
// explicit keystroke. Announced and private-receipt rooms are two requests but one
// report.
func (m Model) markRoomsReadCmd(roomIDs []domain.RoomID, label string) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	announced, quiet := m.splitByReceipt(roomIDs)
	return func() tea.Msg {
		msg := markedReadMsg{label: label, rooms: len(roomIDs)}
		for _, batch := range []struct {
			ids     []domain.RoomID
			private bool
		}{{announced, false}, {quiet, true}} {
			if len(batch.ids) == 0 {
				continue
			}
			result, err := backend.MarkRoomsRead(ctx, batch.ids, batch.private)
			msg.result.Marked += result.Marked
			msg.result.Skipped += result.Skipped
			msg.result.Failed += result.Failed
			if msg.result.FirstError == "" {
				msg.result.FirstError = result.FirstError
			}
			if err != nil && msg.err == nil {
				msg.err = err
			}
		}
		return msg
	}
}

// markedUnreadMsg is the outcome of setting or clearing a room's unread mark.
type markedUnreadMsg struct {
	label  string
	unread bool
	err    error
}

// markRoomUnreadCmd writes the MSC2867 flag; it returns on the Unread stream.
func (m Model) markRoomUnreadCmd(roomID domain.RoomID, unread bool, label string) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		return markedUnreadMsg{label: label, unread: unread, err: backend.MarkRoomUnread(ctx, roomID, unread)}
	}
}

// emojiScoresMsg carries one room's emoji ranking for one kind.
type emojiScoresMsg struct {
	roomID domain.RoomID
	kind   domain.EmojiKind
	scores map[string]int
	err    error
}

// emojiScoresCmd weighs a room's emoji usage, once per kind per room per session.
func (m Model) emojiScoresCmd(roomID domain.RoomID, kind domain.EmojiKind) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	spaceRooms := m.spaceRoomsFor(roomID)
	// The scope is a client setting; the daemon is shared by clients.
	scope := m.glyphs.scope
	return func() tea.Msg {
		scores, err := backend.EmojiScores(ctx, kind, roomID, spaceRooms, scope)
		return emojiScoresMsg{roomID: roomID, kind: kind, scores: scores, err: err}
	}
}

// refusalsMsg carries the emoji bridged networks are known to refuse as reactions.
type refusalsMsg struct {
	refusals []domain.ReactionRefusal
	err      error
}

// refusalsCmd reads that record, at startup and whenever a reaction of ours is removed.
func (m Model) refusalsCmd() tea.Cmd {
	return fetch(m.ctx, m.backend.ReactionRefusals,
		func(refusals []domain.ReactionRefusal, err error) tea.Msg {
			return refusalsMsg{refusals: refusals, err: err}
		})
}

// recordRefusalCmd reports a refusal only the client saw; fire-and-forget.
func (m Model) recordRefusalCmd(protocol, emoji string) tea.Cmd {
	backend := m.backend
	return fire(m.ctx, m.log, "record reaction refusal", func(ctx context.Context) error { return backend.RecordReactionRefusal(ctx, protocol, emoji) })
}

// listenVerifyCmd waits for the next device-verification step.
func (m Model) listenVerifyCmd() tea.Cmd {
	return listen(m.ctx, m.backend.Verifications(),
		func(v domain.Verification) tea.Msg { return verifyMsg{v: v} })
}

// verifyFailedMsg reports a verification control that never reached the daemon.
// Without it the overlay would wait forever for a step that is not coming.
type verifyFailedMsg struct{ err error }

// startVerifyCmd asks this account's other devices to verify this session; the
// returned transaction id addresses every later step.
func (m Model) startVerifyCmd() tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		txnID, err := backend.StartVerification(ctx)
		if err != nil {
			return verifyStartedMsg{err: err}
		}
		return verifyStartedMsg{txnID: txnID}
	}
}

// verifyStartedMsg is the outcome of startVerifyCmd.
type verifyStartedMsg struct {
	txnID string
	err   error
}

func (m Model) acceptVerifyCmd(txnID string) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		if err := backend.AcceptVerification(ctx, txnID); err != nil {
			return verifyFailedMsg{err: err}
		}
		return nil
	}
}

// confirmSASCmd tells the other device the emoji matched; a failure is routed back.
func (m Model) confirmSASCmd(txnID string) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		if err := backend.ConfirmSAS(ctx, txnID); err != nil {
			return verifyFailedMsg{err: err}
		}
		return nil
	}
}

// cancelVerifyCmd abandons a verification. The overlay is already closed, so a
// failure has nothing to report.
func (m Model) cancelVerifyCmd(txnID string) tea.Cmd {
	backend := m.backend
	return fire(m.ctx, m.log, "cancel verification", func(ctx context.Context) error { return backend.CancelVerification(ctx, txnID) })
}

// recordEmojiCmd notes a composed emoji for ranking; fire-and-forget.
func (m Model) recordEmojiCmd(emoji string) tea.Cmd {
	backend := m.backend
	roomID := m.openRoom
	if roomID == "" || emoji == "" {
		return nil
	}
	return fire(m.ctx, m.log, "record emoji use", func(ctx context.Context) error {
		return backend.RecordEmoji(ctx, domain.EmojiComposed, roomID, emoji)
	})
}

// errNoConfigStore: no daemon keeps the config file for this client, so a change can be
// applied but not saved.
var errNoConfigStore = errors.New("no daemon to keep the config file")

// configStore is the daemon, the config file's one writer: a change is made on the
// revision it was read at, and every client hears each change. Optional, as
// followSource is: api.Backend does not carry the config.
type configStore interface {
	GetConfig(ctx context.Context) (config.Snapshot, error)
	UpdateConfig(ctx context.Context, base string, cfg config.Config) (string, error)
	ConfigChanges() <-chan config.Snapshot
}

// configSavedMsg reports the outcome of writing the config back.
type configSavedMsg struct{ err error }

// saveConfigFileCmd has the daemon write a whole configuration (assembled by
// applyConfig). Its place in line is taken now, on the event loop, so snapshots land
// in the order the changes were made.
func (m Model) saveConfigFileCmd(cfg config.Config) tea.Cmd {
	ctx, writer := m.ctx, m.conf.writer
	gen := writer.take()
	return func() tea.Msg { return configSavedMsg{err: writer.save(ctx, gen, cfg)} }
}

// configWriter orders whole-config saves, each on the revision the last one left.
// Each runs on its own goroutine, so without it an older snapshot could land after a
// newer one and silently undo a setting. It also knows which revisions it wrote, so
// the echo of its own save is not taken for news.
type configWriter struct {
	store configStore

	mu      sync.Mutex
	taken   uint64 // the last place in line handed out
	written uint64 // the newest snapshot saved
	rev     string // the revision the next save is made on
	ours    map[string]bool
}

// storeOf is the daemon the writer saves through; nil without one.
func (w *configWriter) storeOf() configStore {
	if w == nil {
		return nil
	}
	return w.store
}

// take is the next place in line. Nil-safe: without a writer, nothing is saved.
func (w *configWriter) take() uint64 {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.taken++
	return w.taken
}

// save has the daemon write cfg, unless a newer snapshot is saved already. Held
// throughout, so saves go one at a time, each on the revision the last left.
func (w *configWriter) save(ctx context.Context, gen uint64, cfg config.Config) error {
	if w == nil || w.store == nil {
		return errNoConfigStore
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if gen <= w.written {
		return nil
	}
	rev, err := w.store.UpdateConfig(ctx, w.rev, cfg)
	if err != nil {
		return err //nolint:wrapcheck // the daemon's own words
	}
	w.written, w.rev = gen, rev
	if w.ours == nil {
		w.ours = map[string]bool{}
	}
	w.ours[rev] = true
	return nil
}

// adopt makes rev, read from the daemon, the revision the next save is made on. news
// is false for one this writer saved itself. A change made elsewhere supersedes the
// saves still in line, which were made over the config before it and would undo it:
// dropped reports whether there were any.
func (w *configWriter) adopt(rev string) (news, dropped bool) {
	if w == nil {
		return false, false
	}
	w.mu.Lock() // waits out a save in flight, whose revision this may be
	defer w.mu.Unlock()
	if w.ours[rev] {
		return false, false
	}
	dropped = w.taken > w.written
	w.rev, w.written = rev, w.taken
	return true, dropped
}

// dndMsg is the daemon's answer to anything touching do-not-disturb: the whole set in
// force or an error. note is the status line, empty for a silent refresh.
type dndMsg struct {
	temps notify.Temps
	note  string
	err   error
}

// pollTickMsg is the periodic check for changes made elsewhere (another terminal's
// mutes, drafts written through kith-mcp).
type pollTickMsg struct{}

// pollInterval is slow on purpose: nothing it catches is urgent, and it is the only
// periodic wakeup in an idle client.
const pollInterval = 60 * time.Second

// pollTickCmd schedules the next look.
func (m Model) pollTickCmd() tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg { return pollTickMsg{} })
}

// dndCmd runs one do-not-disturb call off the event loop and reports the resulting
// set. The daemon is passed to call so callers do not close over m.
func (m Model) dndCmd(note string, call func(Notifications, context.Context) (notify.Temps, error)) tea.Cmd {
	ctx, notifications := m.ctx, m.notifications.backend
	return func() tea.Msg {
		if notifications == nil {
			return nil
		}
		temps, err := call(notifications, ctx)
		return dndMsg{temps: temps, note: note, err: err}
	}
}

// readDNDCmd refreshes what is muted without saying anything about it.
func (m Model) readDNDCmd() tea.Cmd { return m.dndCmd("", Notifications.DND) }

// setDNDCmd puts one temporary rule in force; target names it in the confirmation.
func (m Model) setDNDCmd(rule notify.Rule, target muteTarget) tea.Cmd {
	ctx, notifications, clock := m.ctx, m.notifications.backend, m.prefs.clock
	return func() tea.Msg {
		if notifications == nil {
			return dndMsg{err: errNoDaemon}
		}
		temps, err := notifications.SetDND(ctx, rule)
		if err != nil {
			return dndMsg{err: err}
		}
		return dndMsg{temps: temps, note: mutedNote(target, temps, clock)}
	}
}

// clearAllDNDCmd lifts every entry.
func (m Model) clearAllDNDCmd() tea.Cmd {
	return m.dndCmd("do not disturb off", Notifications.ClearAllDND)
}

// openedMsg reports a failed attempt to open a link.
type openedMsg struct{ err error }

// openLinkCmd hands a link to the configured opener as a single argv element, no
// shell. The caller has already checked the scheme.
func (m Model) openLinkCmd(link string) tea.Cmd {
	ctx, opener := m.ctx, m.prefs.external.open
	return func() tea.Msg {
		// launch waits a moment, so an opener exiting non-zero (no handler for the
		// scheme) is reported rather than looking like success.
		return openedMsg{err: launch(ctx, []string{opener}, link)}
	}
}

// focusedMsg reports a browser that did not come forward.
type focusedMsg struct{ err error }

// focusBrowserCmd raises the browser's window after openLinkCmd, waiting for a
// browser that is still starting (see internal/focus).
func (m Model) focusBrowserCmd() tea.Cmd {
	ctx, configured := m.ctx, m.prefs.external.focus
	return func() tea.Msg {
		return focusedMsg{err: focus.Go(ctx, configured)}
	}
}

// clipboardSinkCmd pipes text to the user's clipboard command on stdin, never argv.
func (m Model) clipboardSinkCmd(text string) tea.Cmd {
	ctx, log := m.ctx, m.log
	command := m.prefs.external.clipboard
	return func() tea.Msg {
		cmd := exec.CommandContext(ctx, "sh", "-c", command) // #nosec G204 -- command is user config; the payload goes on stdin, never interpolated
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Start(); err != nil {
			// The OSC 52 write is the primary path; this is a fallback. Logged.
			log.Warn("clipboard command failed to start", "err", err)
			return nil
		}
		go func() {
			if err := cmd.Wait(); err != nil {
				log.Warn("clipboard command failed", "err", err)
			}
		}()
		return nil
	}
}
