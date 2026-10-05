package slack

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

var (
	// workspaceAddress is a Slack workspace's address: the part before ".slack.com".
	workspaceAddress = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	// teamID is a workspace's ID, as app.slack.com links carry it.
	teamID = regexp.MustCompile(`^T[A-Z0-9]{8,}$`)
	// clientLink is a link into the Slack web client: app.slack.com/client/<team>/….
	clientLink = regexp.MustCompile(`app\.slack\.com/client/(T[A-Z0-9]{8,})(?:/|$)`)
)

// errNotWorkspace is a configured workspace that is no Slack address or ID.
var errNotWorkspace = errors.New("is not a Slack workspace — write its address (acme, for acme.slack.com) " +
	"or its ID (the T… in an app.slack.com/client/ link)")

// isTeamID reports whether workspace is a team ID (T…) rather than an address.
func isTeamID(workspace string) bool { return teamID.MatchString(workspace) }

// addressOf is a workspace as Slack writes it, from what the config or a login may
// say: its address ("acme", "acme.slack.com", "https://acme.slack.com/"), or its team
// ID ("T0123456789", or any app.slack.com/client/T0123456789/… link).
func addressOf(written string) string {
	raw := strings.TrimSpace(written)
	if m := clientLink.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	if isTeamID(raw) {
		return raw
	}
	w := strings.ToLower(raw)
	w = strings.TrimPrefix(strings.TrimPrefix(w, "https://"), "http://")
	w = strings.TrimSuffix(w, "/")
	return strings.TrimSuffix(w, ".slack.com")
}

// checkAddress refuses what is no workspace address or ID (addressOf's result).
func checkAddress(address string) error {
	if !workspaceAddress.MatchString(address) && !isTeamID(address) {
		return fmt.Errorf("%q is not a Slack workspace — write its address (acme, for acme.slack.com), "+
			"or paste a link from the web client (app.slack.com/client/T…)", address)
	}
	return nil
}

// CheckConfig refuses [[slack.account]]s kith could not tell apart or sign in to: a
// missing or repeated name, a workspace that is no Slack address or ID, one listed
// twice. It asks nothing of Slack.
func (a *Adapter) CheckConfig(_ context.Context, cfg config.Config) error {
	records := make([]domain.AccountRecord, 0, len(cfg.Slack.Accounts))
	for _, account := range cfg.Slack.Accounts {
		address := addressOf(account.Workspace)
		record := domain.AccountRecord{Name: account.Name, Field: "workspace", Written: account.Workspace, Key: address}
		if checkAddress(address) != nil {
			record.Bad = errNotWorkspace
		}
		records = append(records, record)
	}
	return domain.CheckAccounts(cfg.Slack.Table(), records) //nolint:wrapcheck // names the record itself
}
