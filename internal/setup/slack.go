package setup

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
)

var (
	// workspaceName is a Slack workspace's address: the part before ".slack.com".
	workspaceName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	// slackTeamID is a workspace's ID, as app.slack.com links carry it.
	slackTeamID = regexp.MustCompile(`^T[A-Z0-9]{8,}$`)
	// clientLink is a link into the Slack web client: app.slack.com/client/<team>/….
	clientLink = regexp.MustCompile(`app\.slack\.com/client/(T[A-Z0-9]{8,})(?:/|$)`)
)

// SlackWorkspace is an account's workspace as Slack writes it, from what the config
// may say: its address ("acme", "acme.slack.com", "https://acme.slack.com/"), or its
// team ID ("T0123456789", or any app.slack.com/client/T0123456789/… link).
func SlackWorkspace(account config.SlackAccount) string {
	raw := strings.TrimSpace(account.Workspace)
	if m := clientLink.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	if slackTeamID.MatchString(raw) {
		return raw
	}
	w := strings.ToLower(raw)
	w = strings.TrimPrefix(strings.TrimPrefix(w, "https://"), "http://")
	w = strings.TrimSuffix(w, "/")
	return strings.TrimSuffix(w, ".slack.com")
}

// SlackAccounts refuses [[slack.account]]s kith could not tell apart or sign in to:
// a missing or repeated name, a workspace that is no Slack address or ID, one listed
// twice.
func SlackAccounts(s config.Slack) error {
	names, workspaces := map[string]bool{}, map[string]bool{}
	for i, account := range s.Accounts {
		where := fmt.Sprintf("slack.account[%d]", i)
		if account.Name == "" {
			return fmt.Errorf("%s: name is empty — `kith login slack <name>` needs one", where)
		}
		workspace := SlackWorkspace(account)
		if !workspaceName.MatchString(workspace) && !slackTeamID.MatchString(workspace) {
			return fmt.Errorf("%s (%s): workspace %q is not a Slack workspace — write its address (acme, "+
				"for acme.slack.com) or its ID (the T… in an app.slack.com/client/ link)", where, account.Name, account.Workspace)
		}
		if names[account.Name] {
			return fmt.Errorf("%s: name %q is used twice", where, account.Name)
		}
		if workspaces[workspace] {
			return fmt.Errorf("%s (%s): workspace %s is listed twice", where, account.Name, workspace)
		}
		names[account.Name], workspaces[workspace] = true, true
	}
	return nil
}

// ErrNoSlackAccount: `kith login slack` was given a name no account has.
var ErrNoSlackAccount = errors.New("no such [[slack.account]]")

// SlackAccount is the account `kith login slack [name]` means: the one named, or the
// only one when there is just one.
func SlackAccount(s config.Slack, name string) (config.SlackAccount, error) {
	if name == "" && len(s.Accounts) == 1 {
		return s.Accounts[0], nil
	}
	known := make([]string, 0, len(s.Accounts))
	for _, account := range s.Accounts {
		if account.Name == name {
			return account, nil
		}
		known = append(known, account.Name)
	}
	if name == "" {
		return config.SlackAccount{}, fmt.Errorf("%w: say which, one of %v", ErrNoSlackAccount, known)
	}
	return config.SlackAccount{}, fmt.Errorf("%w named %q (configured: %v)", ErrNoSlackAccount, name, known)
}
