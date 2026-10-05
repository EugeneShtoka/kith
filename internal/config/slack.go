package config

// Slack is [slack]: kith's own link to Slack workspaces, as the Slack web client is,
// with no Matrix bridge. It runs when it has an account.
type Slack struct {
	Accounts []SlackAccount `toml:"account"`
}

// SlackAccount is one [[slack.account]]: a workspace kith logs in to, under a name
// `kith login slack <name>` takes. Workspace is its address ("acme" or
// "acme.slack.com"), where the login says to sign in, or its ID; the workspace's own
// ID, which every room and message ID carries, comes from the login.
type SlackAccount struct {
	Name      string `toml:"name"`
	Workspace string `toml:"workspace"`
}
