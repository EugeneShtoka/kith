package config

import (
	"strings"
	"time"
)

// Display is [display] in the bridge's config: what kithd reads of how rooms and
// people are shown. The rest of [display] is a device's (DeviceDisplay).
type Display struct {
	// TimeFormat is a domain.TimeStyles name; the dates are domain date patterns.
	// Empty is the default.
	TimeFormat      string        `toml:"time_format"`
	LongDateFormat  string        `toml:"long_date_format"`
	ShortDateFormat string        `toml:"short_date_format"`
	Tracked         Tracked       `toml:"tracked"`
	SpaceRules      []SpaceRule   `toml:"space_rule"`
	Deleted         Deleted       `toml:"deleted"`
	Identities      []Identity    `toml:"identity"`
	Names           []DisplayName `toml:"name"`
	RoomNameRules   *bool         `toml:"room_name_rules"`
	Priority        []string      `toml:"priority"` // home entries (space:, tag:, place:), most preferred first
	Rail            Rail          `toml:"rail"`
	// Unread is what a badge counts (UnreadSources; empty is "messages"): kithd judges
	// each room's unread state by it when it builds the rail.
	Unread string `toml:"unread"`
}

// DeviceDisplay is [display] in device.toml: how this device draws the timeline, room
// list and rail.
type DeviceDisplay struct {
	MaxNameLength int    `toml:"max_name_length"` // sender-name width cap; 0 no limit
	Emoji         Emoji  `toml:"emoji"`
	SkinTone      string `toml:"skin_tone"`      // SkinToneNames; empty is "none"
	ColorMessages bool   `toml:"color_messages"` // tint bodies in the sender's color
	FPS           int    `toml:"fps"`            // repaint cap, MinFPS-MaxFPS; 0 DefaultFPS
	OpenInInsert  *bool  `toml:"open_in_insert_mode"`
	RowNumbers    bool   `toml:"row_numbers"`
	Mouse         *bool  `toml:"mouse"`
	UnreadLine    *bool  `toml:"unread_line"`
	Hyperlinks    *bool  `toml:"hyperlinks"` // OSC 8
	Theme         Theme  `toml:"theme"`
	// NetworkColors names each room in every room list in its network's color
	// (Theme.Networks); a place's [[display.space_rule]] can say otherwise.
	NetworkColors bool `toml:"network_colors"`
	// ShowArchived lists the rooms the archive tag holds in every room list but the
	// archive's own (which always does); nil is true. A place's rule can say otherwise.
	ShowArchived *bool      `toml:"show_archived"`
	Typing       *bool      `toml:"typing"`
	SendTyping   *bool      `toml:"send_typing"`
	SendReceipts *bool      `toml:"send_receipts"`
	ReadDelay    *int       `toml:"read_delay"` // seconds; nil or -1 never, 0 at once
	ReadRules    []ReadRule `toml:"read_rule"`
	FilingSpaces []string   `toml:"filing_spaces"`
	Rooms        Rooms      `toml:"rooms"`
	Reactions    Reactions  `toml:"reactions"`
	Threads      Threads    `toml:"threads"`
	Direction    Direction  `toml:"direction"`
	Media        Media      `toml:"media"`
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
func (d DeviceDisplay) OpenInInsertMode() bool { return enabled(d.OpenInInsert) }

// UseMouse reports whether the wheel scrolls (unset: true).
func (d DeviceDisplay) UseMouse() bool { return enabled(d.Mouse) }

// UseHyperlinks reports whether links are wrapped in OSC 8 (unset: true).
func (d DeviceDisplay) UseHyperlinks() bool { return enabled(d.Hyperlinks) }

// ShowUnreadLine reports whether the unread rule is drawn (unset: true).
func (d DeviceDisplay) ShowUnreadLine() bool { return enabled(d.UnreadLine) }

// ShowTyping reports whether to show who else is typing (unset: true).
func (d DeviceDisplay) ShowTyping() bool { return enabled(d.Typing) }

// SendsTyping reports whether to tell rooms when you are typing (unset: true).
func (d DeviceDisplay) SendsTyping() bool { return enabled(d.SendTyping) }

// SendsReceipts reports whether read receipts are sent (unset: true).
func (d DeviceDisplay) SendsReceipts() bool { return enabled(d.SendReceipts) }

// ReadAfter is how long the cursor must rest on a room before it counts as read without
// being opened; negative is never.
func (d DeviceDisplay) ReadAfter() time.Duration {
	if d.ReadDelay == nil || *d.ReadDelay < 0 {
		return -time.Second
	}
	return time.Duration(*d.ReadDelay) * time.Second
}

// ApplyRoomNameRules reports whether contact name rules also reshape people-named room
// labels (unset: true).
func (d Display) ApplyRoomNameRules() bool { return enabled(d.RoomNameRules) }

// DefaultFPS is the repaint rate when [display] fps is unset: a keystroke echo stays
// imperceptible at half the wakeups of the renderer's default 60.
const DefaultFPS = 30

// MinFPS and MaxFPS bound what the renderer will accept.
const (
	MinFPS = 1
	MaxFPS = 120
)

// FrameRate is the repaint rate to build the program with.
func (d DeviceDisplay) FrameRate() int {
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
	// Networks is [display.theme.networks], each network's color.
	Networks NetworkColors `toml:"networks"`
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
	From   []string `toml:"from"`   // people's IDs; empty anybody
	Notify *bool    `toml:"notify"` // unset follows Tracked.Notify
}

// SpaceRule is one [[display.space_rule]]: a per-space name-display rule.
type SpaceRule struct {
	Space         string `toml:"space"` // a home entry: space:, tag: or place:<name>
	FirstNameOnly bool   `toml:"first_name_only"`
	// NetworkColors names each room in this place's room list in its network's color,
	// or not, whatever Display.NetworkColors says; nil follows it.
	NetworkColors *bool `toml:"network_colors"`
	// ShowArchived lists archived rooms in this place's room list, or not, whatever
	// Display.ShowArchived says; nil follows it.
	ShowArchived *bool `toml:"show_archived"`
	// ShowField is a phonebook field key whose value this place's room list and member
	// list show after a person's name; empty shows none.
	ShowField string `toml:"show_field"`
}

// Empty reports whether the rule sets nothing, so the file need not keep it.
func (r SpaceRule) Empty() bool {
	return !r.FirstNameOnly && r.NetworkColors == nil && r.ShowArchived == nil && r.ShowField == ""
}

// SpaceRule is a place's [[display.space_rule]], if it has one: names reports whether
// a rule's space entry names the place. A space: or tag: rule wins over a place: one.
func (d Display) SpaceRule(names func(entry string) bool) (SpaceRule, bool) {
	for _, byLabel := range []bool{false, true} {
		for _, r := range d.SpaceRules {
			if isPlaceEntry(r.Space) == byLabel && names(r.Space) {
				return r, true
			}
		}
	}
	return SpaceRule{}, false
}

// placePrefix names rail rows by what the rail shows (domain.PlaceEntry).
const placePrefix = "place:"

// isPlaceEntry reports whether an entry is place:<name>.
func isPlaceEntry(entry string) bool {
	entry = strings.TrimSpace(entry)
	return len(entry) >= len(placePrefix) && strings.EqualFold(entry[:len(placePrefix)], placePrefix)
}

// NetworkColorsIn reports whether a place's room list names its rooms in their
// networks' colors: as its rule says (SpaceRule), else as the device's NetworkColors
// does.
func (d Display) NetworkColorsIn(device DeviceDisplay, names func(entry string) bool) bool {
	if r, ok := d.SpaceRule(names); ok && r.NetworkColors != nil {
		return *r.NetworkColors
	}
	return device.NetworkColors
}

// ShowFieldIn is the phonebook field a place shows beside names, "" for none.
func (d Display) ShowFieldIn(names func(entry string) bool) string {
	r, _ := d.SpaceRule(names)
	return r.ShowField
}

// ShowsArchived reports whether room lists show archived rooms ([display] show_archived;
// unset is true).
func (d DeviceDisplay) ShowsArchived() bool { return enabled(d.ShowArchived) }

// ShowsArchivedIn reports whether a place's room list shows archived rooms: as its
// rule says (SpaceRule), else as the device's ShowArchived does.
func (d Display) ShowsArchivedIn(device DeviceDisplay, names func(entry string) bool) bool {
	if r, ok := d.SpaceRule(names); ok && r.ShowArchived != nil {
		return *r.ShowArchived
	}
	return device.ShowsArchived()
}

// NetworkColors is [display.theme.networks]: each network's room-name color, #rrggbb or
// a named color; "" is the default, "none" no color.
type NetworkColors struct {
	WhatsApp       string `toml:"whatsapp"`
	Telegram       string `toml:"telegram"`
	Slack          string `toml:"slack"`
	Signal         string `toml:"signal"`
	Discord        string `toml:"discord"`
	Messenger      string `toml:"messenger"`
	Instagram      string `toml:"instagram"`
	LinkedIn       string `toml:"linkedin"`
	GoogleMessages string `toml:"google_messages"`
	GoogleChat     string `toml:"google_chat"`
	IMessage       string `toml:"imessage"`
	Twitter        string `toml:"twitter"`
	Matrix         string `toml:"matrix"`
}

// defaultNetworkColors are distinct from each other and from the theme's text, accent,
// badge and alert colors; Meta's two networks share one.
var defaultNetworkColors = map[string]string{
	"whatsapp": "teal", "telegram": "blue", "slack": "magenta", "messenger": "orchid",
	"instagram": "orchid", "linkedin": "periwinkle", "google_messages": "coral", "signal": "ultramarine",
}

// byKey maps each network's key (lower-case name, words joined by "_") to its color.
func (n NetworkColors) byKey() map[string]string {
	return map[string]string{
		"whatsapp": n.WhatsApp, "telegram": n.Telegram, "slack": n.Slack, "signal": n.Signal,
		"discord": n.Discord, "messenger": n.Messenger, "instagram": n.Instagram, "linkedin": n.LinkedIn,
		"google_messages": n.GoogleMessages, "google_chat": n.GoogleChat, "imessage": n.IMessage,
		"twitter": n.Twitter, "matrix": n.Matrix,
	}
}

// For is the color a network's rooms are named in, by its name ("WhatsApp", "Google
// Messages"): as set, else its default; "" for none.
func (n NetworkColors) For(network string) string {
	key := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(network)), " ", "_")
	color, known := n.byKey()[key]
	switch {
	case !known:
		return ""
	case color == "":
		return defaultNetworkColors[key]
	case strings.EqualFold(color, "none"):
		return ""
	}
	return color
}

// Set is each network's color as written, by key, for checking.
func (n NetworkColors) Set() map[string]string { return n.byKey() }

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

// The prefixes a DisplayName target can carry.
const (
	NameTargetRoom   = "room:"
	NameTargetSpace  = "space:"
	NameTargetThread = "thread:"
)

// GroupTarget is how a space's rail row is named in the one list: by its own name
// under `space:`. A tag is named in its [[tag]].
func GroupTarget(key string) string { return NameTargetSpace + key }

// SpaceLabels maps each renamed space's name to the label its rail row shows.
func (d Display) SpaceLabels() map[string]string {
	out := map[string]string{}
	for _, entry := range d.Names {
		if key, ok := strings.CutPrefix(entry.Target, NameTargetSpace); ok {
			if entry.Name == "" {
				delete(out, key)
			} else {
				out[key] = entry.Name
			}
		}
	}
	return out
}

// RoomNames maps each named room ID to its name.
func (d Display) RoomNames() map[string]string {
	out := make(map[string]string, len(d.Names))
	for _, entry := range d.Names {
		if isBareRoomID(entry.Target) && entry.Name != "" {
			out[entry.Target] = entry.Name
		}
	}
	return out
}

// isBareRoomID reports whether target carries no NameTarget* prefix; setup refuses any
// other form, so that means a room ID.
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

// HideMine and HideOthers report whether deleted messages are hidden; unset shows them.
func (d Deleted) HideMine() bool   { return d.Mine == DeletedHide }
func (d Deleted) HideOthers() bool { return d.Others == DeletedHide }

// Rail is [display.rail]: group order and visibility, by home entry (space:, tag:,
// place:).
type Rail struct {
	Order         []string `toml:"order"`
	Hidden        []string `toml:"hidden"`
	HideWhenEmpty []string `toml:"hide_when_empty"`
	// NetworkColors names a space or tag whose rooms are all on one network in that
	// network's color ([display.theme.networks]).
	NetworkColors bool `toml:"network_colors"`
}

// Rooms is [display.rooms]: the room list's sort chain, globally and per rail group.
type Rooms struct {
	Sort  []string    `toml:"sort"`
	Rules []RoomsRule `toml:"rule"`
}

// RoomsRule is one [[display.rooms.rule]]; unset fields inherit.
type RoomsRule struct {
	Group string   `toml:"group"` // space:, tag: or place:<name>
	Sort  []string `toml:"sort"`  // replaces the whole chain; empty inherits
}

// ReadRule is one [[display.read_rule]]; unset fields inherit.
type ReadRule struct {
	Match  string `toml:"match"`  // a room ID, space:, tag: or place:
	Sender string `toml:"sender"` // ID of the message's author
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
	Match         string `toml:"match"`            // a room ID, space:, tag: or place:
	MaxInRoomList int    `toml:"max_in_room_list"` // 0 default, negative no cap
}

// defaultThreadRowMark leads a thread's row, pointing at its room's row above.
const defaultThreadRowMark = "↳"

// DefaultThreadsInRoomList is how many of a room's threads are listed before the rest
// are counted into one line.
const DefaultThreadsInRoomList = 5

// Mode is InRoomList normalized; unset or unknown is "unread".
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
