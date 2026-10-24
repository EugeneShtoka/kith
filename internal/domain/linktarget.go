package domain

// A link to a chat kith has not listed (a Telegram @username or phone number, an
// invite) is looked up on its network: the chat to open, or to join first.

// LinkTarget is the chat a link names, as its network found it.
type LinkTarget struct {
	// Room is the chat on the account that looked it up: its ID (empty for an invite
	// whose chat the network names only once joined), name, and whether it is direct.
	Room Room
	// Join means the account is not in the chat: following asks, then joins.
	Join bool
	// About says what joining joins, for the question ("channel, 1204 members").
	About string
}

// LinkNetwork is the network a chat link is for, among those whose chats the daemon
// looks up (Matrix rooms the client joins itself): Telegram's tg: and t.me links.
func LinkNetwork(uri string) (Protocol, bool) {
	if _, ok := ParseTelegramLink(uri); ok {
		return ProtocolTelegram, true
	}
	return "", false
}
