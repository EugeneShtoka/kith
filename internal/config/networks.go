package config

// A NetworkSection is a chat network's table ([whatsapp], [slack], ...) holding its
// accounts, one record each. Its adapter validates it; adding a network takes a
// section, a Config field and a line in Networks.
type NetworkSection interface {
	// Network is the display name: "WhatsApp".
	Network() string
	// Table is its accounts' record table: "whatsapp.account".
	Table() string
	// AccountNames is its accounts' names, in order.
	AccountNames() []string
	// DefaultAccount is the account named by its default, or "".
	DefaultAccount() string
}

// accountNames is each account's name.
func accountNames[A any](accounts []A, name func(A) string) []string {
	out := make([]string, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, name(a))
	}
	return out
}

// Networks is every network's section, in the order the config declares them.
func (c Config) Networks() []NetworkSection {
	return []NetworkSection{c.WhatsApp, c.Slack, c.Telegram, c.Messenger, c.LinkedIn, c.GMessages, c.Signal, c.Instagram}
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

// Network is "LinkedIn".
func (LinkedIn) Network() string { return "LinkedIn" }

// Table is "linkedin.account".
func (LinkedIn) Table() string { return "linkedin.account" }

// Network is "Google Messages".
func (GMessages) Network() string { return "Google Messages" }

// Table is "gmessages.account".
func (GMessages) Table() string { return "gmessages.account" }

// Network is "Signal".
func (Signal) Network() string { return "Signal" }

// Table is "signal.account".
func (Signal) Table() string { return "signal.account" }

// Network is "Instagram".
func (Instagram) Network() string { return "Instagram" }

// Table is "instagram.account".
func (Instagram) Table() string { return "instagram.account" }

// AccountNames is each [[whatsapp.account]]'s name.
func (n WhatsApp) AccountNames() []string {
	return accountNames(n.Accounts, func(a WhatsAppAccount) string { return a.Name })
}

// DefaultAccount is [whatsapp] default.
func (n WhatsApp) DefaultAccount() string { return n.Default }

// AccountNames is each [[slack.account]]'s name.
func (n Slack) AccountNames() []string {
	return accountNames(n.Accounts, func(a SlackAccount) string { return a.Name })
}

// DefaultAccount is [slack] default.
func (n Slack) DefaultAccount() string { return n.Default }

// AccountNames is each [[telegram.account]]'s name.
func (n Telegram) AccountNames() []string {
	return accountNames(n.Accounts, func(a TelegramAccount) string { return a.Name })
}

// DefaultAccount is [telegram] default.
func (n Telegram) DefaultAccount() string { return n.Default }

// AccountNames is each [[messenger.account]]'s name.
func (n Messenger) AccountNames() []string {
	return accountNames(n.Accounts, func(a MessengerAccount) string { return a.Name })
}

// DefaultAccount is [messenger] default.
func (n Messenger) DefaultAccount() string { return n.Default }

// AccountNames is each [[linkedin.account]]'s name.
func (n LinkedIn) AccountNames() []string {
	return accountNames(n.Accounts, func(a LinkedInAccount) string { return a.Name })
}

// DefaultAccount is [linkedin] default.
func (n LinkedIn) DefaultAccount() string { return n.Default }

// AccountNames is each [[gmessages.account]]'s name.
func (n GMessages) AccountNames() []string {
	return accountNames(n.Accounts, func(a GMessagesAccount) string { return a.Name })
}

// DefaultAccount is [gmessages] default.
func (n GMessages) DefaultAccount() string { return n.Default }

// AccountNames is each [[signal.account]]'s name.
func (n Signal) AccountNames() []string {
	return accountNames(n.Accounts, func(a SignalAccount) string { return a.Name })
}

// DefaultAccount is [signal] default.
func (n Signal) DefaultAccount() string { return n.Default }

// AccountNames is each [[instagram.account]]'s name.
func (n Instagram) AccountNames() []string {
	return accountNames(n.Accounts, func(a InstagramAccount) string { return a.Name })
}

// DefaultAccount is [instagram] default.
func (n Instagram) DefaultAccount() string { return n.Default }
