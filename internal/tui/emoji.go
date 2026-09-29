package tui

import (
	"maps"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// emojiState is which emoji this client offers and in what order. A change to set,
// skin, refused or orders is what makes palette stale.
type emojiState struct {
	// set is the working vocabulary ([display.emoji] set plus the user's additions).
	set emojiSet
	// skin is the Fitzpatrick modifier from [display] skin_tone; empty means untoned.
	skin string
	// refused is what bridged networks were seen to reject as reactions, protocol →
	// untoned emoji (refusals.go).
	refused map[string]map[string]bool
	// orders is each room's emoji ranking, fetched once per room per session.
	orders map[domain.RoomID]emojiOrder
	// scope is the palette's frequency scope (room|space|global|static) and static
	// its fallback set. palette is the active 10, computed on room-open and frozen
	// for the visit so key positions hold.
	scope   string
	static  []string
	palette []string
}

// paletteSize is the react palette's slot count, one per react.pick_N binding.
const paletteSize = 10

// defaultReactions is the built-in static palette, used without [display.reactions].
var defaultReactions = []string{"👍", "❤️", "😂", "🎉", "😮", "😢", "🙏", "👀", "🔥", "💯"}

// paletteHint is the one-line legend of the palette with each slot's live pick key
// (by default "1👍 2❤️ … 0💯"); a slot with no key is shown bare.
func (m Model) paletteHint() string {
	var b strings.Builder
	for i, e := range m.glyphs.palette {
		if i > 0 {
			b.WriteByte(' ')
		}
		if i < paletteSize {
			b.WriteString(m.keys.keyHint(scopeReact, actPick1+action(i)))
		}
		b.WriteString(e)
	}
	return b.String()
}

// resolveReaction turns react-prompt input into the reaction key to send: a
// :shortcode: is looked up (case-insensitive), anything else is sent verbatim. An
// unknown shortcode is refused with the nearest names, never sent as literal text.
func (m Model) resolveReaction(s string) (string, bool) {
	s = strings.TrimSpace(s)
	name, isShortcode := shortcodeName(s)
	if !isShortcode {
		return s, true
	}
	if e, ok := m.glyphs.set.byName[name]; ok {
		return e, true
	}
	return m.nearestShortcodes(name), false
}

// shortcodeName is the name inside a :shortcode:, and whether the input was one.
func shortcodeName(s string) (string, bool) {
	if len(s) < 3 || !strings.HasPrefix(s, ":") || !strings.HasSuffix(s, ":") {
		return "", false
	}
	name := strings.ToLower(s[1 : len(s)-1])
	if name == "" || strings.ContainsAny(name, ": \t") {
		return "", false
	}
	return name, true
}

// nearestShortcodes is the refusal message, offering names that start with what was
// typed (a misspelling is usually a truncation: `:dance:` for `:dancer:`).
func (m Model) nearestShortcodes(name string) string {
	var near []string
	for _, candidate := range m.glyphs.set.names {
		if strings.HasPrefix(candidate, name) && candidate != name {
			near = append(near, ":"+candidate+":")
			if len(near) == 3 {
				break
			}
		}
	}
	if len(near) == 0 {
		return "no emoji called :" + name + ":"
	}
	return "no emoji called :" + name + ": — try " + strings.Join(near, " ")
}

// looksLikeEmoji tests shape, not set membership: an emoji has no ASCII letters and
// no colon, which rejects failed shortcodes and text reactions from usage history.
func looksLikeEmoji(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r == ':' || (r < utf8.RuneSelf && unicode.IsLetter(r)) {
			return false
		}
	}
	return true
}

// onlyEmoji is looksLikeEmoji over a list, for the quick palette.
func onlyEmoji(list []string) []string {
	return slices.DeleteFunc(slices.Clone(list), func(e string) bool { return !looksLikeEmoji(e) })
}

// emojiShortcodes is the curated :name: → emoji working set, small enough to browse.
// Several names may share an emoji; the shortest is displayed.
var emojiShortcodes = map[string]string{
	// Hands and gestures
	"+1": "👍", "thumbsup": "👍", "-1": "👎", "thumbsdown": "👎",
	"clap": "👏", "wave": "👋", "ok_hand": "👌", "raised_hands": "🙌", "muscle": "💪",
	"pray": "🙏", "point_up": "☝️", "point_down": "👇", "point_left": "👈",
	"point_right": "👉", "raised_hand": "✋", "heart_hands": "🫶", "handshake": "🤝",
	"fist": "✊", "punch": "👊", "v": "✌️", "crossed_fingers": "🤞", "pinch": "🤏",
	"salute": "🫡", "shrug": "🤷", "facepalm": "🤦", "writing_hand": "✍️",
	"call_me": "🤙", "vulcan": "🖖", "middle_finger": "🖕",

	// Faces
	"smile": "😄", "grin": "😁", "laughing": "😆", "joy": "😂", "rofl": "🤣",
	"blush": "😊", "wink": "😉", "sunglasses": "😎", "sweat_smile": "😅",
	"heart_eyes": "😍", "kissing_heart": "😘", "yum": "😋", "hugs": "🤗",
	"thinking": "🤔", "zipper_mouth": "🤐", "neutral_face": "😐", "expressionless": "😑",
	"unamused": "😒", "roll_eyes": "🙄", "grimacing": "😬", "lying_face": "🤥",
	"relieved": "😌", "pensive": "😔", "sleepy": "😪", "sleeping": "😴",
	"mask": "😷", "nauseated": "🤢", "sneeze": "🤧", "hot": "🥵", "cold": "🥶",
	"dizzy_face": "😵", "exploding_head": "🤯", "cowboy": "🤠", "partying": "🥳",
	"disguised": "🥸", "confused": "😕", "worried": "😟", "frowning": "🙁",
	"open_mouth": "😮", "hushed": "😯", "astonished": "😲", "flushed": "😳",
	"pleading": "🥺", "cry": "😢", "sob": "😭", "scream": "😱", "confounded": "😖",
	"persevere": "😣", "disappointed": "😞", "sweat": "😓", "weary": "😩",
	"tired_face": "😫", "yawning": "🥱", "triumph": "😤", "angry": "😠", "rage": "😡",
	"cursing": "🤬", "smiling_imp": "😈", "clown": "🤡", "nerd": "🤓",
	"monocle": "🧐", "star_struck": "🤩", "zany": "🤪", "upside_down": "🙃",
	"money_mouth": "🤑", "shushing": "🤫", "smirk": "😏",

	// People and creatures
	"poop": "💩", "skull": "💀", "ghost": "👻", "alien": "👽", "robot": "🤖",
	"baby": "👶", "eyes": "👀", "eye": "👁️", "brain": "🧠", "ear": "👂",
	"see_no_evil": "🙈", "hear_no_evil": "🙉", "speak_no_evil": "🙊",
	"dancer": "💃", "man_dancing": "🕺", "walking": "🚶", "running": "🏃",

	// Hearts and symbols
	"heart": "❤️", "orange_heart": "🧡", "yellow_heart": "💛", "green_heart": "💚",
	"blue_heart": "💙", "purple_heart": "💜", "black_heart": "🖤", "white_heart": "🤍",
	"broken_heart": "💔", "two_hearts": "💕", "sparkling_heart": "💖",
	"100": "💯", "check": "✅", "white_check_mark": "✅", "heavy_check_mark": "✔️",
	"x": "❌", "cross_mark": "❌", "warning": "⚠️", "no_entry": "⛔",
	"question": "❓", "exclamation": "❗", "bangbang": "‼️", "recycle": "♻️",
	"ok": "🆗", "new": "🆕", "free": "🆓", "sos": "🆘", "up": "🆙",
	"arrow_up": "⬆️", "arrow_down": "⬇️", "arrow_left": "⬅️", "arrow_right": "➡️",
	"arrows_counterclockwise": "🔄", "repeat": "🔁", "infinity": "♾️",
	"heavy_plus_sign": "➕", "heavy_minus_sign": "➖", "heavy_division_sign": "➗",

	// Objects, work, and things that come up in a dev chat
	"bulb": "💡", "zap": "⚡", "boom": "💥", "fire": "🔥", "sparkles": "✨",
	"star": "⭐", "star2": "🌟", "dizzy": "💫", "rocket": "🚀", "bug": "🐛",
	"beetle": "🪲", "wrench": "🔧", "hammer": "🔨", "nut_and_bolt": "🔩",
	"gear": "⚙️", "toolbox": "🧰", "magnet": "🧲", "test_tube": "🧪",
	"computer": "💻", "desktop": "🖥️", "keyboard": "⌨️", "mouse_three_button": "🖱️",
	"floppy_disk": "💾", "cd": "💿", "printer": "🖨️", "phone": "📱",
	"telephone": "☎️", "battery": "🔋", "plug": "🔌", "satellite": "📡",
	"lock": "🔒", "unlock": "🔓", "key": "🔑", "shield": "🛡️", "bomb": "💣",
	"mag": "🔍", "link": "🔗", "paperclip": "📎", "pushpin": "📌", "round_pushpin": "📍",
	"memo": "📝", "pencil": "✏️", "page_facing_up": "📄", "clipboard": "📋",
	"books": "📚", "book": "📖", "notebook": "📓", "bookmark": "🔖",
	"chart": "📈", "chart_down": "📉", "bar_chart": "📊", "abacus": "🧮",
	"calendar": "📅", "date": "📆", "alarm_clock": "⏰", "hourglass": "⏳",
	"stopwatch": "⏱️", "watch": "⌚", "envelope": "✉️", "inbox_tray": "📥",
	"outbox_tray": "📤", "package": "📦", "label": "🏷️", "moneybag": "💰",
	"credit_card": "💳", "receipt": "🧾", "scissors": "✂️", "wastebasket": "🗑️",
	"broom": "🧹", "soap": "🧼", "bricks": "🧱", "construction": "🚧",
	"trophy": "🏆", "medal": "🏅", "dart": "🎯", "crystal_ball": "🔮",
	"gift": "🎁", "balloon": "🎈", "tada": "🎉", "party": "🥳", "confetti": "🎊",
	"speech_balloon": "💬", "thought_balloon": "💭", "bell": "🔔", "no_bell": "🔕",
	"mega": "📣", "loudspeaker": "📢", "musical_note": "🎵", "headphones": "🎧",
	"microphone": "🎤", "camera": "📷", "video_camera": "📹", "movie": "🎬",
	"tv": "📺", "radio": "📻", "game_die": "🎲", "joystick": "🕹️",
	"puzzle": "🧩", "chess_pawn": "♟️", "art": "🎨", "thread": "🧵",

	// Food and drink
	"coffee": "☕", "tea": "🍵", "beer": "🍺", "beers": "🍻", "wine": "🍷",
	"cocktail": "🍸", "champagne": "🍾", "cup_with_straw": "🥤", "ice": "🧊",
	"pizza": "🍕", "hamburger": "🍔", "fries": "🍟", "hotdog": "🌭", "taco": "🌮",
	"burrito": "🌯", "sushi": "🍣", "ramen": "🍜", "bread": "🍞", "croissant": "🥐",
	"cheese": "🧀", "egg": "🥚", "bacon": "🥓", "salad": "🥗", "popcorn": "🍿",
	"cookie": "🍪", "doughnut": "🍩", "cake": "🎂", "pie": "🥧", "chocolate": "🍫",
	"candy": "🍬", "honey": "🍯", "apple": "🍎", "banana": "🍌", "grapes": "🍇",
	"watermelon": "🍉", "strawberry": "🍓", "cherries": "🍒", "peach": "🍑",
	"pineapple": "🍍", "avocado": "🥑", "carrot": "🥕", "corn": "🌽",
	"hot_pepper": "🌶️", "mushroom": "🍄", "lemon": "🍋",

	// Animals
	"dog": "🐶", "cat": "🐱", "mouse": "🐭", "hamster": "🐹", "rabbit": "🐰",
	"fox": "🦊", "bear": "🐻", "panda": "🐼", "koala": "🐨", "tiger": "🐯",
	"lion": "🦁", "cow": "🐮", "pig": "🐷", "frog": "🐸", "monkey": "🐵",
	"chicken": "🐔", "penguin": "🐧", "bird": "🐦", "duck": "🦆", "owl": "🦉",
	"bat": "🦇", "wolf": "🐺", "horse": "🐴", "unicorn": "🦄", "bee": "🐝",
	"snail": "🐌", "butterfly": "🦋", "ant": "🐜", "spider": "🕷️", "turtle": "🐢",
	"snake": "🐍", "octopus": "🐙", "squid": "🦑", "fish": "🐟", "dolphin": "🐬",
	"whale": "🐳", "shark": "🦈", "crab": "🦀", "elephant": "🐘", "sloth": "🦥",

	// Nature, weather, travel
	"sun": "☀️", "moon": "🌙", "star_moon": "🌛", "cloud": "☁️", "rain": "🌧️",
	"snow": "🌨️", "snowflake": "❄️", "snowman": "⛄", "thunder": "⛈️",
	"rainbow": "🌈", "umbrella": "☔", "wind": "💨", "tornado": "🌪️",
	"ocean": "🌊", "droplet": "💧", "earth": "🌍", "globe": "🌐", "volcano": "🌋",
	"mountain": "⛰️", "desert": "🏜️", "beach": "🏖️", "tent": "⛺",
	"seedling": "🌱", "herb": "🌿", "four_leaf_clover": "🍀", "maple_leaf": "🍁",
	"fallen_leaf": "🍂", "cactus": "🌵", "palm_tree": "🌴", "evergreen": "🌲",
	"cherry_blossom": "🌸", "rose": "🌹", "sunflower": "🌻", "tulip": "🌷",
	"car": "🚗", "taxi": "🚕", "bus": "🚌", "truck": "🚚", "bike": "🚲",
	"motorcycle": "🏍️", "train": "🚆", "metro": "🚇", "airplane": "✈️",
	"helicopter": "🚁", "ship": "🚢", "sailboat": "⛵", "anchor": "⚓",
	"house": "🏠", "office": "🏢", "hospital": "🏥", "bank": "🏦", "school": "🏫",
	"hotel": "🏨", "church": "⛪", "castle": "🏰", "statue_of_liberty": "🗽",
	"traffic_light": "🚦", "stop_sign": "🛑", "world_map": "🗺️", "compass": "🧭",
	"luggage": "🧳", "passport": "🛂", "ticket": "🎫",
}

// emojiSet is the working vocabulary the completion popup and browser offer, in order.
// Tiers: curated (emojiShortcodes), standard (+ every single-glyph Unicode emoji),
// complete (+ ZWJ sequences and flags, which mis-measure without font ligatures).
// Curated names win every collision.
type emojiSet struct {
	// byName maps a shortcode to its emoji; nameOf is each emoji's display name.
	byName map[string]string
	nameOf map[string]string
	// names is every shortcode sorted; all is every distinct emoji by display name.
	names []string
	all   []string
}

// Emoji tiers, as written in the config.
const (
	emojiCurated  = config.EmojiCurated
	emojiStandard = config.EmojiStandard
	emojiComplete = config.EmojiComplete
)

// newEmojiSet builds the vocabulary for a tier plus the user's own additions; an
// unrecognized tier (refused at startup by setup.EmojiTier) means curated.
func newEmojiSet(tier string, extra map[string]string) emojiSet {
	byName := make(map[string]string, len(emojiShortcodes)+len(standardEmoji))
	// Widest first: later writes win, so curated and then user names override.
	if tier == emojiComplete {
		add(byName, sequenceEmoji)
	}
	if tier == emojiComplete || tier == emojiStandard {
		add(byName, standardEmoji)
	}
	add(byName, emojiShortcodes)
	add(byName, extra)

	set := emojiSet{byName: byName}
	set.nameOf = buildShortcodeIndex(byName)
	set.names = slices.Sorted(maps.Keys(byName))
	set.all = distinctEmoji(set)
	return set
}

func add(dst, src map[string]string) {
	for name, emoji := range src {
		if name != "" && emoji != "" {
			dst[name] = emoji
		}
	}
}

// buildShortcodeIndex maps each emoji to its display name: the shortest, then
// alphabetically first, so it is stable between runs.
func buildShortcodeIndex(byName map[string]string) map[string]string {
	out := make(map[string]string, len(byName))
	for name, emoji := range byName {
		best, seen := out[emoji]
		if !seen || len(name) < len(best) || (len(name) == len(best) && name < best) {
			out[emoji] = name
		}
	}
	return out
}

// distinctEmoji is every emoji in the set, ordered by its display shortcode.
func distinctEmoji(set emojiSet) []string {
	seen := make(map[string]bool, len(set.byName))
	out := make([]string, 0, len(set.byName))
	for _, name := range set.names {
		emoji := set.byName[name]
		if set.nameOf[emoji] != name || seen[emoji] {
			continue
		}
		seen[emoji] = true
		out = append(out, emoji)
	}
	return out
}

// EmojiCells is every cell the browser would draw for a display configuration, toned
// and presentation-reconciled. Exported for cmd/emoji-probe, which asks the terminal
// how wide each really is.
func EmojiCells(display config.Display) ([]string, error) {
	tone, err := setup.SkinTone(display.SkinTone)
	if err != nil {
		return nil, err
	}
	tier, err := setup.EmojiTier(display.Emoji.Set)
	if err != nil {
		return nil, err
	}
	set := newEmojiSet(tier, display.Emoji.Extra)
	cells := make([]string, 0, len(set.all))
	for _, emoji := range set.all {
		cells = append(cells, emojiCell(toned(emoji, tone)))
	}
	return cells, nil
}
