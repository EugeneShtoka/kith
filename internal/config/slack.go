package config

// Slack is [slack]: kith's own link to Slack workspaces, as the Slack web client is,
// with no Matrix bridge. Off unless enabled.
type Slack struct {
	Enabled  bool           `toml:"enabled"`
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
