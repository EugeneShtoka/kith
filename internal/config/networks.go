package config

// A NetworkSection is a chat network's own table ([whatsapp], [slack], …): the
// accounts kith signs in to there, one record each. The settings screen lists every
// network through it; whether a section is right is its adapter's to say (kithd asks
// each one), so a new network is its section, a field of Config and a line in
// Networks.
type NetworkSection interface {
	// Network is the network's name as a sentence says it: "WhatsApp".
	Network() string
	// Table is its accounts' record table: "whatsapp.account".
	Table() string
}

// Networks is every network's section, in the order the config declares them.
func (c Config) Networks() []NetworkSection {
	return []NetworkSection{c.WhatsApp, c.Slack, c.Telegram}
}

// Network is "WhatsApp".
func (WhatsApp) Network() string { return "WhatsApp" }

// Table is "whatsapp.account".
func (WhatsApp) Table() string { return "whatsapp.account" }

// Network is "Slack".
func (Slack) Network() string { return "Slack" }

// Table is "slack.account".
func (Slack) Table() string { return "slack.account" }

// Network is "Telegram".
func (Telegram) Network() string { return "Telegram" }

// Table is "telegram.account".
func (Telegram) Table() string { return "telegram.account" }
