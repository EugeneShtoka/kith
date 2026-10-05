package setup

import (
	"fmt"

	"github.com/EugeneShtoka/kith/internal/config"
)

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
		workspace := account.Address()
		if config.CheckSlackAddress(workspace) != nil {
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
