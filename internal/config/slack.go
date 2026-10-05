package config

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	// slackAddress is a Slack workspace's address: the part before ".slack.com".
	slackAddress = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	// slackTeamID is a workspace's ID, as app.slack.com links carry it.
	slackTeamID = regexp.MustCompile(`^T[A-Z0-9]{8,}$`)
	// slackClientLink is a link into the Slack web client: app.slack.com/client/<team>/….
	slackClientLink = regexp.MustCompile(`app\.slack\.com/client/(T[A-Z0-9]{8,})(?:/|$)`)
)

// IsSlackTeamID reports whether workspace is a team ID (T…) rather than an address.
func IsSlackTeamID(workspace string) bool { return slackTeamID.MatchString(workspace) }

// Slack is [slack]: kith's own link to Slack workspaces, as the Slack web client is,
// with no Matrix bridge. It runs when it has an account.
type Slack struct {
	Accounts []SlackAccount `toml:"account"`
}

// SlackAccount is one [[slack.account]]: a workspace kith logs in to, under a name
// `kith login slack <name>` takes. Workspace is its address ("acme" or
// "acme.slack.com"), where the login says to sign in; the workspace's own ID, which
// every room and message ID carries, comes from the login.
type SlackAccount struct {
	Name      string `toml:"name"`
	Workspace string `toml:"workspace"`
}

// Address is the account's workspace as Slack writes it, from what the config may
// say: its address ("acme", "acme.slack.com", "https://acme.slack.com/"), or its team
// ID ("T0123456789", or any app.slack.com/client/T0123456789/… link).
func (a SlackAccount) Address() string {
	raw := strings.TrimSpace(a.Workspace)
	if m := slackClientLink.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	if IsSlackTeamID(raw) {
		return raw
	}
	w := strings.ToLower(raw)
	w = strings.TrimPrefix(strings.TrimPrefix(w, "https://"), "http://")
	w = strings.TrimSuffix(w, "/")
	return strings.TrimSuffix(w, ".slack.com")
}

// CheckSlackAddress refuses what is no workspace address or ID (Address's result).
func CheckSlackAddress(address string) error {
	if !slackAddress.MatchString(address) && !IsSlackTeamID(address) {
		return fmt.Errorf("%q is not a Slack workspace — write its address (acme, for acme.slack.com), "+
			"or paste a link from the web client (app.slack.com/client/T…)", address)
	}
	return nil
}
