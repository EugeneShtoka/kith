package slack

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	// teamName is the workspace's own name, from sign-in.
	teamName string
	client   *slackgo.Client
}

// ErrNoAccount is a sign-in for a name no [[slack.account]] has.
var ErrNoAccount = errors.New("slack: no such [[slack.account]]")

// clientFor is a Slack client with a workspace's session: the token, and the cookie
// it is only good together with.
func clientFor(c Credentials) *slackgo.Client {
	return slackgo.New(c.Token, slackgo.OptionCookie("d", c.Cookie))
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
	client := clientFor(creds)
	who, err := client.AuthTestContext(ctx)
	if err != nil {
		return api.SlackSignedIn{}, fmt.Errorf("slack: check the session: %w", err)
	}
	if err := sameWorkspace(account, who.URL); err != nil {
		return api.SlackSignedIn{}, err
	}
	creds.Team, creds.User = who.TeamID, who.UserID
	if err := saveCredentials(a.secrets, account.Name, creds); err != nil {
		return api.SlackSignedIn{}, err
	}
	w := &workspace{account: account, creds: creds, teamName: who.Team, client: client, signIn: a.newSignIn(account.Name)}
	if !a.adopt(w) {
		return api.SlackSignedIn{}, fmt.Errorf("%w named %q: it left the config while signing in", ErrNoAccount, name)
	}
	if _, err := a.list(ctx, w); err != nil {
		a.log.Warn("list the workspace after signing in failed", "account", name, "err", err)
	}
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
	client := clientFor(creds)
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
	w := &workspace{account: account, creds: creds, teamName: who.Team, client: client, signIn: signIn}
	if !a.adopt(w) {
		return true // signed in again, or removed, meanwhile
	}
	if _, err := a.list(ctx, w); err != nil {
		a.log.Warn("list the workspace failed", "account", account.Name, "err", err)
	}
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
	current := w.signIn == a.signIns[w.account.Name] &&
		slices.ContainsFunc(a.accounts, func(k Account) bool { return k.Name == w.account.Name })
	if current {
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
	names := map[string]string{}
	if partners := dmPartners(conversations); len(partners) > 0 {
		users, err := w.client.GetUsersInfoContext(ctx, partners...)
		if err != nil {
			// Unnamed DMs still list, under their IDs, rather than none at all.
			a.log.Warn("read the DM partners' names failed", "account", w.account.Name, "err", err)
		} else {
			for i := range *users {
				names[(*users)[i].ID] = userName((*users)[i])
			}
		}
	}
	l := listed(w.creds.Team, w.teamName, conversations, names)
	if err := a.save(ctx, w, l); err != nil {
		return nil, err
	}
	return l.rooms, nil
}

// save writes a workspace's listing over its cached rooms, space and DM members.
func (a *Adapter) save(ctx context.Context, w *workspace, l listing) error {
	if a.cache == nil {
		return nil
	}
	owner := domain.AccountRooms(domain.ProtocolSlack, w.creds.Team)
	if err := a.cache.SaveRooms(ctx, owner, l.rooms); err != nil {
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
