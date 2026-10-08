package config

import (
	"strings"
	"time"
)

// Display is [display]: how the timeline, room list and rail are drawn.
type Display struct {
	MaxNameLength int    `toml:"max_name_length"` // sender-name width cap; 0 no limit
	Emoji         Emoji  `toml:"emoji"`
	SkinTone      string `toml:"skin_tone"` // SkinToneNames; empty is "none"
	// TimeFormat is a domain.TimeStyles name; the dates are domain date patterns.
	// Empty is the default.
	TimeFormat      string        `toml:"time_format"`
	LongDateFormat  string        `toml:"long_date_format"`
	ShortDateFormat string        `toml:"short_date_format"`
	ColorMessages   bool          `toml:"color_messages"` // tint bodies in the sender's color
	FPS             int           `toml:"fps"`            // repaint cap, MinFPS-MaxFPS; 0 DefaultFPS
	OpenInInsert    *bool         `toml:"open_in_insert_mode"`
	RowNumbers      bool          `toml:"row_numbers"`
	Mouse           *bool         `toml:"mouse"`
	UnreadLine      *bool         `toml:"unread_line"`
	Hyperlinks      *bool         `toml:"hyperlinks"` // OSC 8
	Theme           Theme         `toml:"theme"`
	Tracked         Tracked       `toml:"tracked"`
	Typing          *bool         `toml:"typing"`
	SendTyping      *bool         `toml:"send_typing"`
	SendReceipts    *bool         `toml:"send_receipts"`
	ReadDelay       *int          `toml:"read_delay"` // seconds; nil or -1 never, 0 at once
	ReadRules       []ReadRule    `toml:"read_rule"`
	FilingSpaces    []string      `toml:"filing_spaces"`
	SpaceRules      []SpaceRule   `toml:"space_rule"`
	Deleted         Deleted       `toml:"deleted"`
	Identities      []Identity    `toml:"identity"`
	Names           []DisplayName `toml:"name"`
	RoomNameRules   *bool         `toml:"room_name_rules"`
	Unread          string        `toml:"unread"`   // UnreadSources; empty is "messages"
	Priority        []string      `toml:"priority"` // space names and tag:<name>s, most preferred first
	Rooms           Rooms         `toml:"rooms"`
	Rail            Rail          `toml:"rail"`
	Media           Media         `toml:"media"`
	Reactions       Reactions     `toml:"reactions"`
	Threads         Threads       `toml:"threads"`
	Direction       Direction     `toml:"direction"`
}

// Direction is [display.direction]: which rooms read right to left, the sender column
// mirrored onto the right. Auto guesses each room from its messages as it opens; rtl
// and ltr set places by hand (place vocabulary; the narrowest entry wins, and wins
// over the guess).
type Direction struct {
	Auto bool     `toml:"auto"`
	RTL  []string `toml:"rtl"`
	LTR  []string `toml:"ltr"`
}

// OpenInInsertMode reports whether opening a room starts in the composer (unset: true).
func (d Display) OpenInInsertMode() bool { return enabled(d.OpenInInsert) }

// UseMouse reports whether the wheel scrolls (unset: true).
func (d Display) UseMouse() bool { return enabled(d.Mouse) }

// UseHyperlinks reports whether links are wrapped in OSC 8 (unset: true).
func (d Display) UseHyperlinks() bool { return enabled(d.Hyperlinks) }

// ShowUnreadLine reports whether the unread rule is drawn (unset: true).
func (d Display) ShowUnreadLine() bool { return enabled(d.UnreadLine) }

// ShowTyping reports whether to show who else is typing (unset: true).
func (d Display) ShowTyping() bool { return enabled(d.Typing) }

// SendsTyping reports whether to tell rooms when you are typing (unset: true).
func (d Display) SendsTyping() bool { return enabled(d.SendTyping) }

// SendsReceipts reports whether your read position is announced to the room (unset:
// true).
func (d Display) SendsReceipts() bool { return enabled(d.SendReceipts) }

// ReadAfter is how long the cursor must rest on a room before it counts as read without
// being opened; negative is never.
func (d Display) ReadAfter() time.Duration {
	if d.ReadDelay == nil || *d.ReadDelay < 0 {
		return -time.Second
	}
	return time.Duration(*d.ReadDelay) * time.Second
}

// ApplyRoomNameRules reports whether contact name rules also reshape people-named room
// labels (unset: true).
func (d Display) ApplyRoomNameRules() bool { return enabled(d.RoomNameRules) }

// DefaultFPS is the repaint rate used when [display] fps is unset: fast enough that a
// keystroke's echo (at most one frame away) is not perceptible, and half the wakeups of
// the renderer's own default of 60.
const DefaultFPS = 30

// MinFPS and MaxFPS bound what the renderer will accept.
const (
	MinFPS = 1
	MaxFPS = 120
)

// FrameRate is the repaint rate to build the program with.
func (d Display) FrameRate() int {
	if d.FPS == 0 {
		return DefaultFPS
	}
	return d.FPS
}

// Emoji is [display.emoji]: how much of the standard set to offer, plus your own.
type Emoji struct {
	Set   string            `toml:"set"`   // EmojiTiers; empty is "curated"
	Extra map[string]string `toml:"extra"` // shortcode → emoji
}

// Theme is [display.theme]: a preset plus per-role color overrides.
type Theme struct {
	Preset        string `toml:"preset"`
	Accent        string `toml:"accent"`
	Border        string `toml:"border"`
	Text          string `toml:"text"`
	Muted         string `toml:"muted"`
	Cursor        string `toml:"cursor"`
	Badge         string `toml:"badge"`
	BadgeAlert    string `toml:"badge_alert"`
	SelectedBG    string `toml:"selected_bg"`
	SelectedDimBG string `toml:"selected_dim_bg"`
}

// Overrides is the theme's per-role colors keyed by their config names, which is what
// theme.Resolve takes.
func (t Theme) Overrides() map[string]string {
	out := map[string]string{}
	for role, value := range map[string]string{
		"accent": t.Accent, "border": t.Border, "text": t.Text, "muted": t.Muted,
		"cursor": t.Cursor, "badge": t.Badge, "badge_alert": t.BadgeAlert,
		"selected_bg": t.SelectedBG, "selected_dim_bg": t.SelectedDimBG,
	} {
		if value != "" {
			out[role] = value
		}
	}
	return out
}

// Tracked is [display.tracked]: words marked wherever they are said.
type Tracked struct {
	Words  []string      `toml:"words"` // globs: word, word*, *word*, *word
	Rules  []TrackedRule `toml:"rule"`
	Notify bool          `toml:"notify"`
}

// TrackedRule is one [[display.tracked.rule]]: tracked words with a range.
type TrackedRule struct {
	Words  []string `toml:"words"`
	In     []string `toml:"in"`     // place vocabulary; empty everywhere
	Except []string `toml:"except"` // place vocabulary; wins over In
	From   []string `toml:"from"`   // MXIDs; empty anybody
	Notify *bool    `toml:"notify"` // unset follows Tracked.Notify
}

// SpaceRule is one [[display.space_rule]]: a per-space name-display rule.
type SpaceRule struct {
	Space         string `toml:"space"` // the space's rail label
	FirstNameOnly bool   `toml:"first_name_only"`
}

// Identity is one [[display.identity]]: several accounts, on any networks, shown as one
// person.
type Identity struct {
	Alias string   `toml:"alias"`
	Color string   `toml:"color"` // "#rrggbb" or a named color
	IDs   []string `toml:"ids"`   // user IDs, on any network
}

// DisplayName is one [[display.name]]: a name you gave something.
type DisplayName struct {
	Target string `toml:"target"` // a bare room ID, or a NameTarget* prefix + key
	Name   string `toml:"name"`
}

// The prefixes a DisplayName target can carry. Spelled here so the parser, the writers
// and the documentation cannot drift.
const (
	NameTargetRoom   = "room:"
	NameTargetSpace  = "space:"
	NameTargetThread = "thread:"
)

// GroupTarget is how a space's rail row is named in the one list: by its own name
// under `space:`. A tag is named in its [[tag]].
func GroupTarget(key string) string { return NameTargetSpace + key }

// NameFor is the name given to one target, or empty.
func (d Display) NameFor(target string) string {
	name := ""
	for _, entry := range d.Names {
		if entry.Target == target {
			name = entry.Name
		}
	}
	return name
}

// RoomNames is every room-ID entry as a map, for the callers that index by room rather
// than asking one at a time — the daemon's scope index, chiefly, which has to resolve a
// rule written against a name you chose.
func (d Display) RoomNames() map[string]string {
	out := make(map[string]string, len(d.Names))
	for _, entry := range d.Names {
		if isBareRoomID(entry.Target) && entry.Name != "" {
			out[entry.Target] = entry.Name
		}
	}
	return out
}

// isBareRoomID reports whether a name's target is a room ID on its own, not one of the
// NameTarget* forms. That is all a target can otherwise be: setup refuses the rest.
func isBareRoomID(target string) bool {
	if target == "" {
		return false
	}
	for _, prefix := range []string{NameTargetRoom, NameTargetSpace, NameTargetThread} {
		if strings.HasPrefix(target, prefix) {
			return false
		}
	}
	return true
}

// SetName adds, replaces or removes one entry, and returns the new list.
func SetName(names []DisplayName, target, name string) []DisplayName {
	out := make([]DisplayName, 0, len(names)+1)
	for _, entry := range names {
		if entry.Target != target {
			out = append(out, entry)
		}
	}
	if name != "" {
		out = append(out, DisplayName{Target: target, Name: name})
	}
	return out
}

// Deleted is [display.deleted]: what the timeline does with deleted messages.
type Deleted struct {
	Mine        string `toml:"mine"`   // DeletedModes; empty is "show"
	Others      string `toml:"others"` // DeletedModes; empty is "show"
	KeepDeleted bool   `toml:"keep"`
}

// Keep reports whether deleted messages are kept, which is false unless asked for.
func (d Deleted) Keep() bool { return d.KeepDeleted }

// HideMine and HideOthers read the two settings, defaulting to showing both for a
// config that does not mention them.
func (d Deleted) HideMine() bool   { return d.Mine == DeletedHide }
func (d Deleted) HideOthers() bool { return d.Others == DeletedHide }

// Rail is [display.rail]: group order, visibility and labels, by group key.
type Rail struct {
	Order         []string `toml:"order"`
	Hidden        []string `toml:"hidden"`
	HideWhenEmpty []string `toml:"hide_when_empty"`
}

// Rooms is [display.rooms]: the room list's sort chain, globally and per rail group.
type Rooms struct {
	Sort  []string    `toml:"sort"`
	Rules []RoomsRule `toml:"rule"`
}

// RoomsRule is one [[display.rooms.rule]]; unset fields inherit.
type RoomsRule struct {
	Group string   `toml:"group"` // a rail group key
	Sort  []string `toml:"sort"`  // replaces the whole chain; empty inherits
}

// ReadRule is one [[display.read_rule]]; unset fields inherit.
type ReadRule struct {
	Match  string `toml:"match"`  // a room ID or a space's name
	Sender string `toml:"sender"` // MXID of the message's author
	Send   *bool  `toml:"send"`
	Delay  *int   `toml:"delay"` // seconds; -1 never, 0 at once
}

// Reactions is [display.reactions]: the react palette (the 1-9,0 quick picks).
type Reactions struct {
	Scope  string   `toml:"scope"`
	Static []string `toml:"static"`
}

// Threads is [display.threads]: where a room's threads appear in the room list.
type Threads struct {
	InRoomList    string       `toml:"in_room_list"`     // ThreadListings; empty is "unread"
	MaxInRoomList int          `toml:"max_in_room_list"` // 0 default, negative no cap
	Rules         []ThreadRule `toml:"rule"`
	Marker        string       `toml:"marker"` // empty is defaultThreadRowMark
}

// ThreadRule is one [[display.threads.rule]].
type ThreadRule struct {
	Match         string `toml:"match"`            // a room ID or a space's name
	MaxInRoomList int    `toml:"max_in_room_list"` // 0 default, negative no cap
}

// defaultThreadRowMark leads a thread's row: the shape says "inside the row above" by
// pointing at it.
const defaultThreadRowMark = "↳"

// DefaultThreadsInRoomList is how many of a room's threads are listed before the rest
// are counted into one line.
const DefaultThreadsInRoomList = 5

// Mode is InRoomList as one of the three values, defaulting an unset or unknown
// setting to "unread" rather than refusing to draw a room list.
func (t Threads) Mode() string {
	switch strings.ToLower(strings.TrimSpace(t.InRoomList)) {
	case ThreadsAll:
		return ThreadsAll
	case ThreadsNever:
		return ThreadsNever
	default:
		return ThreadsUnread
	}
}

// RowMark is the marker that leads a thread's row, defaulting when unset.
func (t Threads) RowMark() string {
	if mark := strings.TrimSpace(t.Marker); mark != "" {
		return mark
	}
	return defaultThreadRowMark
}
