package slack

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"sync"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// listPage is how many conversations one users.conversations page asks for (Slack's
// most).
const listPage = 1000

// conversationTypes is every kind of conversation a person is in.
var conversationTypes = []string{"public_channel", "private_channel", "mpim", "im"}

// workspace is one signed-in account's connection.
type workspace struct {
	account Account
	creds   Credentials
	// signIn is the account's sign-in the session is from (Adapter.signIns).
	signIn int
	// teamName is the workspace's own name, and handle the person's in it, from
	// sign-in.
	teamName, handle string
	client           *slackgo.Client

	// rtm is the live websocket, set while live runs (under mu): typing notices go out
	// on it.
	rtm *slackgo.RTM

	// done closes when the connection is let go: signed in again, removed, stopped.
	done      chan struct{}
	closeOnce sync.Once
	// catching is held while the workspace is caught up (see catchUp).
	catching sync.Mutex

	mu sync.Mutex
	// people and channels name the workspace's users and conversations by ID, as
	// far as they are known; joined is the conversations the last listing named.
	people   map[string]string
	channels map[string]string
	joined   map[string]bool
}

// newWorkspace is a connection on a session; client talks to Slack with it.
func newWorkspace(account Account, creds Credentials, teamName string, client *slackgo.Client, signIn int) *workspace {
	return &workspace{
		account: account, creds: creds, teamName: teamName, client: client, signIn: signIn,
		done: make(chan struct{}), people: map[string]string{}, channels: map[string]string{},
	}
}

// close lets the connection go; its live events stop.
func (w *workspace) close() { w.closeOnce.Do(func() { close(w.done) }) }

// names is how w writes a message's references.
func (w *workspace) names() names {
	return names{
		team: w.creds.Team, me: w.creds.User,
		user: func(id string) string {
			w.mu.Lock()
			defer w.mu.Unlock()
			return w.people[id]
		},
		channel: func(id string) string {
			w.mu.Lock()
			defer w.mu.Unlock()
			return w.channels[id]
		},
	}
}

// unnamed is the users among these w has no name for, each once.
func (w *workspace) unnamed(users []string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	unknown := slices.DeleteFunc(slices.Clone(users), func(u string) bool { return w.people[u] != "" })
	slices.Sort(unknown)
	return slices.Compact(unknown)
}

// knowPerson keeps a user's name.
func (w *workspace) knowPerson(user, name string) {
	if name == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.people[user] = name
}

// knowChannels keeps the listed conversations, and their names.
func (w *workspace) knowChannels(rooms []domain.Room) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.joined = make(map[string]bool, len(rooms))
	for i := range rooms {
		id := domain.ParseID(string(rooms[i].ID)).Native
		w.joined[id] = true
		if rooms[i].Name != "" {
			w.channels[id] = rooms[i].Name
		}
	}
}

// listedNow is the conversations the last listing named, in order.
func (w *workspace) listedNow() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Sorted(maps.Keys(w.joined))
}

// ErrNoAccount is a sign-in for a name no [[slack.account]] has.
var ErrNoAccount = errors.New("slack: no such [[slack.account]]")

// clientFor is a Slack client with a workspace's session: the token, and the cookie
// it is only good together with. Its own log goes to the adapter's, without the
// session (see slackLog).
func (a *Adapter) clientFor(c Credentials) *slackgo.Client {
	return slackgo.New(c.Token, slackgo.OptionCookie("d", cookieValue(c.Cookie)), slackgo.OptionLog(slackLog{log: a.log}))
}

// cookieValue is the `d` cookie decoded. A browser shows it URL-encoded ("%2B", "%2F")
// and slackgo.OptionCookie encodes what it is given, so the copied value would reach
// Slack encoded twice and be refused. PathUnescape, not QueryUnescape: the decoded
// value has '+' in it, which must stay a '+'. A value that does not decode is left as
// it was.
func cookieValue(cookie string) string {
	if v, err := url.PathUnescape(cookie); err == nil {
		return v
	}
	return cookie
}

// SignInSlack takes a session for the named account (its token and `d` cookie, from a
// browser signed in to the workspace), checks it with Slack and against the
// workspace the account names, keeps it, and connects the account.
func (a *Adapter) SignInSlack(ctx context.Context, name, token, cookie string) (api.SlackSignedIn, error) {
	i := slices.IndexFunc(a.accountsNow(), func(acc Account) bool { return acc.Name == name })
	if i < 0 {
		return api.SlackSignedIn{}, fmt.Errorf("%w named %q", ErrNoAccount, name)
	}
	account := a.accountsNow()[i]
	creds := Credentials{Token: token, Cookie: cookie}
	client := a.clientFor(creds)
	who, err := client.AuthTestContext(ctx)
	if err != nil {
		return api.SlackSignedIn{}, fmt.Errorf("slack: check the session: %w", err)
	}
	if err := sameWorkspace(account, who.URL, who.TeamID); err != nil {
		return api.SlackSignedIn{}, err
	}
	creds.Team, creds.User = who.TeamID, who.UserID
	if err := saveCredentials(a.secrets, account.Name, creds); err != nil {
		return api.SlackSignedIn{}, err
	}
	w := newWorkspace(account, creds, who.Team, client, a.newSignIn(account.Name))
	w.handle = who.User
	if !a.adopt(w) {
		return api.SlackSignedIn{}, fmt.Errorf("%w named %q: it left the config while signing in", ErrNoAccount, name)
	}
	if _, err := a.list(ctx, w); err != nil {
		a.log.Warn("list the workspace after signing in failed", "account", name, "err", err)
	}
	a.goLive(w)
	return api.SlackSignedIn{Workspace: who.Team, User: who.User}, nil
}

// Retrying a workspace Slack cannot be reached for: from firstRetry, doubling to
// lastRetry.
const (
	firstRetry = 5 * time.Second
	lastRetry  = 5 * time.Minute
)

// connect opens a signed-in account's session, trying again while Slack cannot be
// reached. Slack is asked who it is first, so a session Slack has ended reads as
// signed out rather than as an outage.
func (a *Adapter) connect(ctx context.Context, account Account, creds Credentials) {
	signIn := a.currentSignIn(account.Name)
	for wait := firstRetry; ; wait = min(2*wait, lastRetry) {
		done := a.connectOnce(ctx, account, creds, signIn)
		if done {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// connectOnce is one try; false when Slack could not be reached.
func (a *Adapter) connectOnce(ctx context.Context, account Account, creds Credentials, signIn int) bool {
	a.session(account, Connecting, "")
	client := a.clientFor(creds)
	who, err := client.AuthTestContext(ctx)
	if isSignedOut(err) {
		a.session(account, SignedOut, "Slack ended the session; run `kith login slack "+account.Name+"` again")
		return true
	}
	if err != nil {
		a.log.Warn("reach Slack failed", "account", account.Name, "err", err)
		a.session(account, Connecting, "Slack cannot be reached: "+err.Error())
		return ctx.Err() != nil
	}
	w := newWorkspace(account, creds, who.Team, client, signIn)
	w.handle = who.User
	if !a.adopt(w) {
		return true // signed in again, or removed, meanwhile
	}
	if _, err := a.list(ctx, w); err != nil {
		a.log.Warn("list the workspace failed", "account", account.Name, "err", err)
	}
	a.goLive(w)
	return true
}

// isSignedOut reports whether Slack refused the session itself.
func isSignedOut(err error) bool {
	if slackErr, ok := errors.AsType[slackgo.SlackErrorResponse](err); ok {
		switch slackErr.Err {
		case "invalid_auth", "not_authed", "token_revoked", "account_inactive", "token_expired":
			return true
		}
	}
	return false
}

// newSignIn starts an account's next sign-in, superseding any connection still
// being made on an older session.
func (a *Adapter) newSignIn(account string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.signIns[account]++
	return a.signIns[account]
}

// currentSignIn is the account's latest sign-in.
func (a *Adapter) currentSignIn(account string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.signIns[account]
}

// adopt makes w the account's connection and says it is connected — unless the
// account was signed in again since w's session (a connection made on the older one
// would replace the newer), or is no longer configured. It reports whether it did.
func (a *Adapter) adopt(w *workspace) bool {
	a.mu.Lock()
	current := w.signIn == a.signIns[w.account.Name] && !a.stopped &&
		slices.ContainsFunc(a.accounts, func(k Account) bool { return k.Name == w.account.Name })
	if current {
		if old, ok := a.workspaces[w.account.Name]; ok && old != w {
			old.close()
		}
		a.workspaces[w.account.Name] = w
	}
	a.mu.Unlock()
	if current {
		a.session(w.account, Connected, "")
	}
	return current
}

// connected is the workspaces with a session now.
func (a *Adapter) connected() []*workspace {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*workspace, 0, len(a.workspaces))
	for _, w := range a.workspaces {
		out = append(out, w)
	}
	return out
}

// list fetches a workspace's conversations and the people its DMs are with, and
// writes them over the account's cached rooms and space.
func (a *Adapter) list(ctx context.Context, w *workspace) ([]domain.Room, error) {
	a.refreshing.Lock()
	defer a.refreshing.Unlock()
	fetched := time.Now()
	var conversations []slackgo.Channel
	for cursor := ""; ; {
		page, next, err := w.client.GetConversationsForUserContext(ctx, &slackgo.GetConversationsForUserParameters{
			Cursor: cursor, Types: conversationTypes, Limit: listPage, ExcludeArchived: true,
		})
		if err != nil {
			return nil, fmt.Errorf("slack: list %s's conversations: %w", w.account.Name, err)
		}
		conversations = append(conversations, page...)
		if cursor = next; cursor == "" {
			break
		}
	}
	// Unnamed DMs still list, under their IDs or handles, rather than none at all.
	groups := a.groupMembers(ctx, w, groupDMs(conversations))
	people := dmPartners(conversations)
	for _, members := range groups {
		people = append(people, members...)
	}
	a.learnPeople(ctx, w, people)
	names := map[string]string{}
	for _, u := range people {
		names[u] = w.names().user(u)
	}
	l := listed(w.creds.Team, w.teamName, self{user: w.creds.User, handle: w.handle}, conversations, groups, names)
	w.knowChannels(l.rooms)
	if err := a.save(ctx, w, l, fetched); err != nil {
		return nil, err
	}
	return l.rooms, nil
}

// groupMembers asks Slack who is in each group DM. One it cannot say is left out, and
// that group DM named by handles.
func (a *Adapter) groupMembers(ctx context.Context, w *workspace, ids []string) map[string][]string {
	groups := make(map[string][]string, len(ids))
	for _, id := range ids {
		var members []string
		err := waitingOut(ctx, func() (err error) {
			members, _, err = w.client.GetUsersInConversationContext(ctx, &slackgo.GetUsersInConversationParameters{ChannelID: id, Limit: membersPage})
			if err != nil {
				return fmt.Errorf("slack: members of %s: %w", id, err)
			}
			return nil
		})
		if err != nil {
			a.log.Warn("read a group DM's members failed", "account", w.account.Name, "err", err)
			continue
		}
		groups[id] = members
	}
	return groups
}

// save writes a workspace's listing, fetched then, over its cached rooms, space and DM
// members. A room a message arrived in since the listing was fetched (a channel just
// joined, a DM just begun) is kept, though the listing does not name it: the sweep
// would take its history.
func (a *Adapter) save(ctx context.Context, w *workspace, l listing, fetched time.Time) error {
	if a.cache == nil {
		return nil
	}
	a.listing.Lock()
	defer a.listing.Unlock()
	owner := domain.AccountRooms(domain.ProtocolSlack, w.creds.Team)
	kept, err := a.keptRooms(ctx, owner, l.rooms, fetched)
	if err != nil {
		return err
	}
	if err := a.cache.SaveRooms(ctx, owner, append(slices.Clone(l.rooms), kept...)); err != nil {
		return fmt.Errorf("slack: cache %s's rooms: %w", w.account.Name, err)
	}
	if err := a.cache.SaveSpaces(ctx, owner, []domain.Space{l.space}); err != nil {
		return fmt.Errorf("slack: cache %s's workspace: %w", w.account.Name, err)
	}
	for id, members := range l.members {
		if err := a.cache.SaveMembers(ctx, id, members); err != nil {
			return fmt.Errorf("slack: cache %s's members: %w", id, err)
		}
	}
	if a.onRoomsChanged != nil {
		a.onRoomsChanged()
	}
	return nil
}

// keptRooms is an owner's cached rooms a listing fetched then must not sweep: those
// heard from since. Caller holds listing.
func (a *Adapter) keptRooms(ctx context.Context, owner domain.RoomOwner, listed []domain.Room, fetched time.Time) ([]domain.Room, error) {
	rooms, err := a.cache.Rooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("slack: read cached rooms: %w", err)
	}
	return slices.DeleteFunc(rooms, func(r domain.Room) bool {
		return !owner.Owns(r.ID) || a.heard[r.ID].Before(fetched) ||
			slices.ContainsFunc(listed, func(l domain.Room) bool { return l.ID == r.ID })
	}), nil
}
