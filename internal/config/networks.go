package config

// A NetworkSection is a chat network's table ([whatsapp], [slack], ...) holding its
// accounts, one record each. Its adapter validates it; adding a network takes a
// section, a Config field and a line in Networks.
type NetworkSection interface {
	// Network is the display name: "WhatsApp".
	Network() string
	// Table is its accounts' record table: "whatsapp.account".
	Table() string
}

// Networks is every network's section, in the order the config declares them.
func (c Config) Networks() []NetworkSection {
	return []NetworkSection{c.WhatsApp, c.Slack, c.Telegram, c.Messenger}
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

// Network is "Messenger".
func (Messenger) Network() string { return "Messenger" }

// Table is "messenger.account".
func (Messenger) Table() string { return "messenger.account" }
