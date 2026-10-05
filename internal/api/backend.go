// Package api defines the Backend interface the TUI talks to. It keeps mautrix-go
// out of the presentation layer.
package api

import (
	"context"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Backend is the full client-side surface: every role below. A consumer that needs
// only a few methods takes a role, or declares its own narrow interface.
//
// Logging in and resuming a session are not part of it. Only the process that owns
// the session does that, on matrix.InProc directly: the daemon at startup, and
// `kith login`. A socket client could otherwise replace the client the daemon
// syncs with.
type Backend interface {
	Sync
	Rooms
	Spaces
	Timeline
	Unread
	Reactions
	Media
	Members
	Membership
	Search
	Drafts
	Threads
	Verification
	Keys
	Assist
	Maintenance
	Identity
}

// Identity is who this person is on the networks the daemon serves.
type Identity interface {
	// Selves is every ID that is this person: the Matrix account and its configured
	// identities, and each linked WhatsApp account's phone number and LID. It grows as
	// accounts log in, so a client asks again when the room list changes.
	Selves(ctx context.Context) ([]string, error)
}

// Sync is the /sync loop and the streams it feeds.
type Sync interface {
	// Start runs until ctx is canceled or Stop is called. It blocks.
	Start(ctx context.Context) error
	Stop()
	Messages() <-chan domain.Message
	// Activity streams typing and read positions of others; not cached.
	Activity() <-chan domain.Activity
	// Attached reports the streams being lost (false) and re-subscribed (true),
	// i.e. the daemon restarting. On true, clients re-read their caches. Nothing is
	// sent for the first attachment.
	Attached() <-chan bool
}

// Rooms is the room list, read markers and room account data. Cached reads are
// instant and may be stale; Refresh* fetch from the homeserver.
type Rooms interface {
	// Rooms returns the cached joined rooms. Empty means a cold cache.
	Rooms(ctx context.Context) ([]domain.Room, error)
	// RefreshRooms fetches joined rooms from the homeserver and updates the cache.
	RefreshRooms(ctx context.Context) ([]domain.Room, error)
	// MarkRead sends a receipt for eventID; private sends m.read.private. The
	// resulting unread change arrives on the Unread stream.
	MarkRead(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, private bool) error
	// MarkRoomsRead receipts the newest locally known event of each room, reporting
	// per-room outcomes. A non-nil error means nothing could be attempted.
	MarkRoomsRead(ctx context.Context, roomIDs []domain.RoomID, private bool) (domain.ReadResult, error)
	// MarkRoomUnread sets or clears MSC2867 m.marked_unread (room account data).
	MarkRoomUnread(ctx context.Context, roomID domain.RoomID, unread bool) error
	// StarMessage adds or removes a private bookmark stored in room account data.
	StarMessage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, starred bool) error
	// StarredIn returns a room's starred messages, newest star first (cache read).
	StarredIn(ctx context.Context, roomID domain.RoomID) ([]domain.EventID, error)
	// SpamRooms returns the rooms a spam filter caught (cache read).
	SpamRooms(ctx context.Context) ([]domain.SpamVerdict, error)
	// MarkSpam records or clears one room's verdict in room account data.
	MarkSpam(ctx context.Context, verdict domain.SpamVerdict) error
	// LastMessages is each room's latest cached message time. Rooms with no cached
	// history are absent (unknown), not zero.
	LastMessages(ctx context.Context) (map[domain.RoomID]time.Time, error)
	// CanonicalParent is the space a room's canonical m.space.parent names; empty
	// when there is none.
	CanonicalParent(ctx context.Context, roomID domain.RoomID) (domain.SpaceID, error)
}

// Spaces is the space hierarchy.
type Spaces interface {
	// Spaces returns the cached joined spaces with their direct children.
	Spaces(ctx context.Context) ([]domain.Space, error)
	// RefreshSpaces fetches the hierarchy from the homeserver and updates the cache.
	RefreshSpaces(ctx context.Context) ([]domain.Space, error)
	// AddToSpace writes m.space.child and a non-canonical m.space.parent. Returns
	// ErrNoSpaceParent when only the parent was refused.
	AddToSpace(ctx context.Context, spaceID domain.SpaceID, roomID domain.RoomID) error
	// RemoveFromSpace empties both events; same ErrNoSpaceParent contract.
	RemoveFromSpace(ctx context.Context, spaceID domain.SpaceID, roomID domain.RoomID) error
}

// Timeline is scrollback and sending.
type Timeline interface {
	// CachedTimeline returns a room's most recent cached messages.
	CachedTimeline(ctx context.Context, roomID domain.RoomID) ([]domain.Message, error)
	// MessageHistory returns every version of a message oldest first, plus its
	// deletion (taken from the server, which knows when it happened).
	MessageHistory(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]domain.Revision, domain.Deletion, error)
	// Timeline fetches one page of history, older-first within the page. Empty from
	// is the newest page; the returned Next is empty at the start of the room.
	Timeline(ctx context.Context, roomID domain.RoomID, from string, limit int) (domain.TimelinePage, error)
	// FetchEvent fetches one event by ID, e.g. a thread root older than the cache.
	FetchEvent(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.Message, error)
	// Redact deletes a message. Redacting others' messages checks the room's
	// redact level first. The change arrives on the sync stream.
	Redact(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, reason string) error
	// Send posts a message; the draft carries text, mentions, reply and thread.
	Send(ctx context.Context, roomID domain.RoomID, draft domain.Draft) error
	// SendTyping sets or clears the typing notice for timeout.
	SendTyping(ctx context.Context, roomID domain.RoomID, typing bool, timeout time.Duration) error
	// SendFile uploads (encrypting where needed) and posts a file. path is absolute
	// on the shared machine; empty caption uses the filename.
	SendFile(ctx context.Context, roomID domain.RoomID, path, caption string) error
}

// Unread is per-room unread state, as a cache read and a live stream.
type Unread interface {
	CachedUnread(ctx context.Context) ([]domain.Unread, error)
	Unread() <-chan domain.Unread
}

// Reactions is m.annotation reactions, and ranking the emoji the user picks,
// separately for reactions and composed text.
type Reactions interface {
	CachedReactions(ctx context.Context, roomID domain.RoomID) ([]domain.Reaction, error)
	Reactions() <-chan domain.ReactionUpdate
	SendReaction(ctx context.Context, roomID domain.RoomID, target domain.EventID, key string) error
	// ReactionRefusals is the learned set of emoji bridged networks reject.
	ReactionRefusals(ctx context.Context) ([]domain.ReactionRefusal, error)
	// RecordReactionRefusal notes a refusal the client saw as a failed send.
	RecordReactionRefusal(ctx context.Context, protocol, emoji string) error
	// EmojiScores scores every emoji of kind for a room. scope is "room" (room 10,
	// its spaces 3, anywhere 1), "space" (no room term) or "global"; unknown means
	// "room". spaceRooms is the room's space siblings.
	EmojiScores(ctx context.Context, kind domain.EmojiKind, roomID domain.RoomID, spaceRooms []domain.RoomID, scope string) (map[string]int, error)
	// RecordEmoji notes one pick. Reactions record themselves from sync.
	RecordEmoji(ctx context.Context, kind domain.EmojiKind, roomID domain.RoomID, emoji string) error
}

// Media resolves a message's media bytes, from the disk cache or by downloading
// (and decrypting) and caching.
type Media interface {
	LoadImage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]byte, error)
}

// Members is who is in a room, and the colors they are drawn in.
type Members interface {
	// Members returns cached members by display name, at most limit (0 = all).
	Members(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error)
	// RefreshMembers fetches membership from the homeserver and replaces the cache.
	RefreshMembers(ctx context.Context, roomID domain.RoomID) ([]domain.Member, error)
	// MentionCandidates orders members for the mention dropdown: recent speakers,
	// then most mentioned, then alphabetical.
	MentionCandidates(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error)
	// SearchSenders ranks people who posted in rooms for `from:`.
	SearchSenders(ctx context.Context, rooms domain.RoomSet, limit int) ([]domain.Member, error)
	// DirectCandidates ranks people to DM, excluding existing DMs (from m.direct,
	// which only the backend sees) and this user.
	DirectCandidates(ctx context.Context, limit int) ([]domain.Member, error)
	// SenderSlots is a room's persisted color slots by identity group; persisted
	// because assignment is ordinal.
	SenderSlots(ctx context.Context, roomID domain.RoomID) (map[string]int, error)
	// SaveSenderSlots records slots; already stored slots win.
	SaveSenderSlots(ctx context.Context, roomID domain.RoomID, slots map[string]int) error
}

// Membership is our own membership (invites and the answers to them) plus
// moderation. The Invites stream carries the whole current set each time.
type Membership interface {
	CachedInvites(ctx context.Context) ([]domain.Room, error)
	Invites() <-chan []domain.Room
	// JoinRoom joins by ID or alias (also accepts an invite). via are servers to
	// try when this homeserver does not know the room.
	JoinRoom(ctx context.Context, roomIDOrAlias string, via []string) (domain.RoomID, error)
	// CreateRoom makes a room or space. A non-empty ID with an error means the
	// room exists but a later step (filing into its space) failed.
	CreateRoom(ctx context.Context, spec domain.NewRoom) (domain.RoomID, error)
	// InviteUser, KickUser, BanUser and UnbanUser check power levels first and
	// return ErrNoPower naming the level needed.
	InviteUser(ctx context.Context, roomID domain.RoomID, userID string) error
	KickUser(ctx context.Context, roomID domain.RoomID, userID, reason string) error
	BanUser(ctx context.Context, roomID domain.RoomID, userID, reason string) error
	UnbanUser(ctx context.Context, roomID domain.RoomID, userID string) error
	// LeaveRoom leaves a room or rejects an invite.
	LeaveRoom(ctx context.Context, roomID domain.RoomID) error
}

// Search is search over the local cache and the lookups beside it.
type Search interface {
	// SearchMessages finds cached messages in req.Rooms, newest first; terms are
	// literal, so no input is invalid.
	SearchMessages(ctx context.Context, req domain.SearchRequest) ([]domain.SearchHit, error)
	// RoomsWith finds rooms all userIDs are in, most recently active first, among rooms
	// before limit is applied.
	RoomsWith(ctx context.Context, userIDs []string, rooms domain.RoomSet, limit int) ([]domain.Room, error)
	// RoomEncryption reports which rooms are encrypted; unknown reports as
	// encrypted (the safe direction).
	RoomEncryption(ctx context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]bool, error)
	// MessagesAround returns a message with its neighbors, oldest first.
	MessagesAround(ctx context.Context, roomID domain.RoomID, event domain.EventID, before, after int) ([]domain.Message, error)
}

// Drafts is the composer's drafts, kept per room.
type Drafts interface {
	// ReplaceDraft stores a room's draft (an empty one removes it), but only while the
	// stored draft is still over, as its writer read it (a zero over: none stored).
	// saved is false when another writer came between. Every write is conditional:
	// the TUI and kith-mcp both write drafts, and neither may drop the other's words.
	ReplaceDraft(ctx context.Context, draft, over domain.StoredDraft) (saved bool, err error)
	// Drafts returns every stored draft, newest first.
	Drafts(ctx context.Context) ([]domain.StoredDraft, error)
}

// Seat makes one window at a time the daemon's: two windows typing into one draft
// cannot be merged word for word, and nobody needs two open. Only the daemon's windows
// take it; kith-mcp and one-shot commands need none.
type Seat interface {
	// TakeSeat makes this window the one the daemon serves, for as long as it runs.
	// Refused with ErrSeatTaken, naming the other window, unless force: that window is
	// then told to save its drafts and quit, and this call answers once it has (or
	// has not answered in a few seconds).
	TakeSeat(ctx context.Context, force bool, where domain.SeatHolder) error
	// SeatLost is told when another window takes the seat: save the drafts and quit.
	SeatLost() <-chan struct{}
}

// Threads is threads, whose read position (MSC3771) is separate from the room's.
type Threads interface {
	// ListThreads returns a room's threads, newest activity first.
	ListThreads(ctx context.Context, roomID domain.RoomID) ([]domain.Thread, error)
	// ThreadPage fetches one page of a thread's history from the homeserver, for
	// threads longer than the cached window. Empty from is the newest page.
	ThreadPage(ctx context.Context, roomID domain.RoomID, root domain.EventID, from string, limit int) (domain.TimelinePage, error)
	// MarkThreadRead sends a threaded receipt, leaving the room's position alone.
	MarkThreadRead(ctx context.Context, roomID domain.RoomID, root, eventID domain.EventID, private bool) error
}

// Verification is interactive SAS device verification. Controls return
// ErrNoEncryption when E2EE is off.
type Verification interface {
	Verifications() <-chan domain.Verification
	// StartVerification asks this account's other devices to verify this one and
	// returns the transaction ID.
	StartVerification(ctx context.Context) (string, error)
	AcceptVerification(ctx context.Context, txnID string) error
	ConfirmSAS(ctx context.Context, txnID string) error
	CancelVerification(ctx context.Context, txnID string) error
}

// Keys is the room-key backup and key files.
type Keys interface {
	// RestoreKeyBackup unlocks secret storage and imports every backed-up room key.
	// Returns ErrNoEncryption, ErrNoKeyBackup or ErrBadRecoveryKey.
	RestoreKeyBackup(ctx context.Context, secret string) (int, error)
	// ExportRoomKeys returns an encrypted Matrix key-export file. Bytes, not a
	// path: the daemon cannot write where the user wants it. Returns
	// ErrNoEncryption or ErrNoRoomKeys.
	ExportRoomKeys(ctx context.Context, passphrase string) ([]byte, error)
	// ImportRoomKeys imports a key export, returning new and total sessions.
	// Returns ErrNoEncryption or ErrBadKeyFile.
	ImportRoomKeys(ctx context.Context, passphrase string, data []byte) (imported, total int, err error)
	// BootstrapKeyBackup creates cross-signing keys, secret storage, a backup
	// version and a first upload. password is the account password (UIA). Returns
	// ErrKeyBackupExists, ErrNoEncryption or ErrBadPassword. The recovery key
	// returned is the only copy.
	BootstrapKeyBackup(ctx context.Context, password string) (domain.KeyBackup, error)
}

// Assist is word completion, spelling and the language models, with the
// dictionaries and weights they install.
type Assist interface {
	// CompleteWord completes a lower-cased prefix from the FTS vocabulary, topped
	// up from the language's frequency list. Results are folded; callers re-case.
	CompleteWord(ctx context.Context, req domain.CompleteRequest) ([]domain.WordCandidate, error)
	// ModelTask runs one language-model task. Refusals (not configured, room not
	// opted in, rate cap) come back in the result, not as errors.
	ModelTask(ctx context.Context, req domain.ModelRequest) (domain.ModelResult, error)
	// DetectLanguages suggests installable spelling dictionaries for the scripts in
	// the cached corpus, most-written first.
	DetectLanguages(ctx context.Context) (domain.SpellSuggestion, error)
	// InstallDictionary fetches a dictionary, verifying the manifest's pinned hash.
	InstallDictionary(ctx context.Context, tag string) error
	// InstallFrequencies fetches a language's word counts (for the rare-word check)
	// and uses them without a restart.
	InstallFrequencies(ctx context.Context, tag string) error
	// DetectModel reports whether the local completion model is worth offering.
	DetectModel(ctx context.Context) (domain.ModelSuggestion, error)
	// InstallModel fetches pinned weights and points the running layer at them.
	InstallModel(ctx context.Context, tag string) error
	// CheckSpelling returns misspellings in text with byte ranges and suggestions.
	// The backend tokenizes. Returns ErrSpellUnavailable when there is no engine.
	CheckSpelling(ctx context.Context, text string) ([]domain.Misspelling, error)
	// LearnWord accepts a word, persisting it to the personal dictionary when
	// forever. Returns ErrSpellUnavailable when there is no engine.
	LearnWord(ctx context.Context, word string, forever bool) error
	// AllowRareWord permanently silences the rare-word hint for word.
	AllowRareWord(ctx context.Context, word string) error
}

// Maintenance is operations on the stores only the process owning them can
// perform.
type Maintenance interface {
	// ClearCache empties the local cache; the sync loop refills it.
	ClearCache(ctx context.Context) error
}

// WhatsAppLink links WhatsApp accounts to kith. Only the daemon pairs, because it owns
// the WhatsApp store (the devices' keys); `kith login whatsapp` asks it.
type WhatsAppLink interface {
	// PairWhatsApp links the named [[whatsapp.account]]: code is told the pairing code
	// to type on the phone, and the account's person ID is returned once the phone
	// accepted it. ErrNetworkOff when WhatsApp is not enabled.
	PairWhatsApp(ctx context.Context, account string, code func(string) error) (string, error)
}

// SlackSignIn signs Slack workspaces in. Only the daemon signs in, because it keeps
// the session and connects on it; `kith login slack` asks it.
type SlackSignIn interface {
	// SignInSlack takes a session for the named [[slack.account]]: its token and `d`
	// cookie, from a browser signed in to the workspace. Slack checks them, and they
	// must be the account's workspace. ErrNetworkOff when Slack is not enabled.
	SignInSlack(ctx context.Context, account, token, cookie string) (SlackSignedIn, error)
}

// SlackSignedIn is who and where a Slack sign-in landed, by name.
type SlackSignedIn struct {
	Workspace, User string
}

// TelegramLogin logs Telegram accounts in. Only the daemon logs in, because it keeps
// the session and connects on it; `kith login telegram` asks it. Logging in is two
// calls, since Telegram sends its code only once it has the number: the code is asked
// for, then given.
type TelegramLogin interface {
	// SendTelegramCode has Telegram send the named [[telegram.account]] a login code,
	// through app (the zero App: kith's own, when this build carries one), and says
	// where it went. It starts the account's login over, ending one under way.
	// ErrNetworkOff when the daemon runs no Telegram account.
	SendTelegramCode(ctx context.Context, account string, app TelegramApp) (TelegramCodeSent, error)
	// SignInTelegram finishes the account's login with the code Telegram sent, and its
	// two-step verification password when it has one: ErrPasswordNeeded asks for it,
	// and the call is made again with it. A wrong code or password can be tried again.
	SignInTelegram(ctx context.Context, account, code, password string) (TelegramSignedIn, error)
}

// TelegramApp is the app a Telegram login goes through: an api_id and api_hash from
// my.telegram.org. The zero App is kith's own.
type TelegramApp struct {
	ID   int
	Hash string
}

// TelegramCodeSent is where Telegram sent a login code: "the Telegram app", "SMS", …
type TelegramCodeSent struct {
	Via string
}

// TelegramSignedIn is whom a Telegram login landed as: a name, and the account's
// person ID.
type TelegramSignedIn struct {
	Name, ID string
}

// MatrixLogin logs Matrix in. Only the daemon logs in, because it owns the session
// and starts Matrix on it; `kith login` asks it.
type MatrixLogin interface {
	// LoginMatrix logs the config's Matrix user in with password and saves the
	// session. ErrNetworkOff when Matrix is not configured.
	LoginMatrix(ctx context.Context, password string) (MatrixLoggedIn, error)
}

// MatrixLoggedIn is a Matrix login's outcome.
type MatrixLoggedIn struct {
	UserID, DeviceID string
	// Started is true when Matrix started on the session at once; false when it was
	// saved for the daemon's next start.
	Started bool
}
