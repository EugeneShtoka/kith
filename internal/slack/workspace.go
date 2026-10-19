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

	"github.com/EugeneShtoka/kith/internal/domain"
)

// listPage is the users.conversations page size (Slack's maximum).
const listPage = 1000

var conversationTypes = []string{"public_channel", "private_channel", "mpim", "im"}

// workspace is one signed-in account's connection.
type workspace struct {
	account Account
	creds   Credentials
	// signIn is the account's sign-in the session is from (Adapter.signIns).
	signIn int
	// teamName is the workspace's name and handle the person's, from sign-in.
	teamName, handle string
	client           *slackgo.Client

	// rtm is the live websocket while live runs (under mu); typing notices go out on it.
	rtm *slackgo.RTM

	// done closes when the connection is let go (re-sign-in, removal, stop).
	done      chan struct{}
	closeOnce sync.Once
	// catching is held while the workspace is caught up (see catchUp).
	catching sync.Mutex
	// filesReread runs refetchFileMessages once per daemon, not per reconnect: a message
	// naming an unloadable file (a link, a deleted one) would be reread every time.
	filesReread sync.Once

	mu sync.Mutex
	// people and channels are known names by ID; joined is the last listing's conversations.
	people   map[string]string
	channels map[string]string
	joined   map[string]bool
	// general is the last listing's general channel; the cache does not keep it.
	general string
	// phones is each user's number, where their profile has it in international form.
	phones map[string]string
}

func newWorkspace(account Account, creds Credentials, teamName string, client *slackgo.Client, signIn int) *workspace {
	return &workspace{
		account: account, creds: creds, teamName: teamName, client: client, signIn: signIn,
		done: make(chan struct{}), people: map[string]string{}, channels: map[string]string{},
	}
}

// close lets the connection go; its live events stop.
func (w *workspace) close() { w.closeOnce.Do(func() { close(w.done) }) }

// names resolves a message's references for w.
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

// unnamed is the given users w has no name for, deduplicated.
func (w *workspace) unnamed(users []string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	unknown := slices.DeleteFunc(slices.Clone(users), func(u string) bool { return w.people[u] != "" })
	slices.Sort(unknown)
	return slices.Compact(unknown)
}

// knowUser keeps a user's name and, if given in international form, their number.
func (w *workspace) knowUser(u *slackgo.User) {
	w.knowPerson(u.ID, userName(*u))
	if digits, ok := domain.PhoneIn(u.Profile.Phone); ok {
		w.knowPhone(u.ID, digits)
	}
}

func (w *workspace) knowPhone(user, digits string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.phones == nil {
		w.phones = map[string]string{}
	}
	w.phones[user] = digits
}

// directory is the known users, by ID and chosen name, linked to their numbers.
func (w *workspace) directory() ([]domain.PersonName, []domain.PersonLink) {
	w.mu.Lock()
	defer w.mu.Unlock()
	names := make([]domain.PersonName, 0, len(w.people))
	for user, name := range w.people {
		names = append(names, domain.PersonName{ID: personID(w.creds.Team, user), Name: name, Rank: domain.RankChosen})
	}
	links := make([]domain.PersonLink, 0, len(w.phones))
	for user, digits := range w.phones {
		links = append(links, domain.PersonLink{ID: personID(w.creds.Team, user), Other: domain.PhoneID(digits)})
	}
	return names, links
}

func (w *workspace) knowPerson(user, name string) {
	if name == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.people[user] = name
}

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

// knowGeneral notes the last listing's general channel.
func (w *workspace) knowGeneral(channel string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.general = channel
}

// generalRoom is the general channel's room ID, "" until a listing names it.
func (w *workspace) generalRoom() domain.RoomID {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.general == "" {
		return ""
	}
	return roomID(w.creds.Team, w.general)
}

// listedNow is the last listing's conversations, sorted.
func (w *workspace) listedNow() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Sorted(maps.Keys(w.joined))
}

// ErrNoAccount is a sign-in for a name no [[slack.account]] has.
var ErrNoAccount = errors.New("slack: no such [[slack.account]]")

// clientFor is a client on a session: the token plus the `d` cookie it only works
// with. Its log goes to the adapter's, without the session (see slackLog).
func (a *Adapter) clientFor(c Credentials) *slackgo.Client {
	return slackgo.New(c.Token, slackgo.OptionCookie("d", cookieValue(c.Cookie)), slackgo.OptionLog(slackLog{log: a.log}))
}

// cookieValue decodes the `d` cookie: browsers show it URL-encoded and OptionCookie
// encodes again, which Slack refuses. PathUnescape keeps '+' as is. An undecodable
// value is returned unchanged.
func cookieValue(cookie string) string {
	if v, err := url.PathUnescape(cookie); err == nil {
		return v
	}
	return cookie
}

// landing is the workspace and user a sign-in landed on, by name.
type landing struct{ workspace, user string }

// signIn verifies a browser session (token and `d` cookie) with Slack and against the
// account's workspace, stores it, and connects the account.
func (a *Adapter) signIn(ctx context.Context, name, token, cookie string) (landing, error) {
	i := slices.IndexFunc(a.accountsNow(), func(acc Account) bool { return acc.Name == name })
	if i < 0 {
		return landing{}, fmt.Errorf("%w named %q", ErrNoAccount, name)
	}
	account := a.accountsNow()[i]
	creds := Credentials{Token: token, Cookie: cookie}
	client := a.clientFor(creds)
	who, err := client.AuthTestContext(ctx)
	if err != nil {
		return landing{}, fmt.Errorf("slack: check the session: %w", err)
	}
	if err := sameWorkspace(account, who.URL, who.TeamID); err != nil {
		return landing{}, err
	}
	creds.Team, creds.User = who.TeamID, who.UserID
	if err := saveCredentials(a.secrets, account.Name, creds); err != nil {
		return landing{}, err
	}
	w := newWorkspace(account, creds, who.Team, client, a.newSignIn(account.Name))
	w.handle = who.User
	if !a.adopt(w) {
		return landing{}, fmt.Errorf("%w named %q: it left the config while signing in", ErrNoAccount, name)
	}
	if _, err := a.list(ctx, w); err != nil {
		a.log.Warn("list the workspace after signing in failed", "account", name, "err", err)
	}
	a.goLive(w)
	return landing{workspace: who.Team, user: who.User}, nil
}

// Retry backoff while Slack is unreachable: firstRetry, doubling up to lastRetry.
const (
	firstRetry = 5 * time.Second
	lastRetry  = 5 * time.Minute
)

// connect opens an account's session, retrying while Slack is unreachable. auth.test
// comes first so an ended session reads as signed out, not an outage.
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

// newSignIn starts the account's next sign-in, superseding connections on older ones.
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

// adopt makes w the account's connection unless the account signed in again since w's
// session or is no longer configured. It reports whether it did.
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

// list fetches a workspace's conversations and DM peers and writes them over its
// cached rooms and space.
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
	// Unnamed DMs still list, under IDs or handles.
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
	w.knowGeneral(l.general)
	if err := a.save(ctx, w, l, fetched); err != nil {
		return nil, err
	}
	return l.rooms, nil
}

// groupMembers asks who is in each group DM; one that fails is left out and named by
// handles.
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

// save writes a listing fetched at fetched over the cached rooms, space and DM members.
// A room that got a message since the fetch is kept though unlisted, or the sweep would
// drop its history.
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

// keptRooms is the owner's cached rooms heard from since fetched, which the listing must
// not sweep. Caller holds listing.
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
