package config

// Slack is [slack]: kith's direct Slack connection (no bridge); active when it has an
// account.
type Slack struct {
	Accounts []SlackAccount `toml:"account"`
}

// SlackAccount is one [[slack.account]], named for `kith login slack <name>`. Workspace
// is its address ("acme", "acme.slack.com") or ID; the team ID in room IDs comes from
// the login.
type SlackAccount struct {
	Name      string `toml:"name"`
	Workspace string `toml:"workspace"`
}
