package config

// GMessages is [gmessages]: kith's direct Google Messages connection (no bridge),
// paired with an Android phone; active when it has an account.
type GMessages struct {
	Accounts []GMessagesAccount `toml:"account"`
}

// GMessagesAccount is one [[gmessages.account]], named for `kith login gmessages
// <name>`. Which phone it is comes from pairing.
type GMessagesAccount struct {
	Name string `toml:"name"`
}
