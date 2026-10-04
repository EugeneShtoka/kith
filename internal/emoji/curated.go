// Package emoji is kith's emoji by name: a curated working set with the names people
// type (and Slack and GitHub use), and every Unicode emoji by its CLDR name, generated.
package emoji

// Curated is the curated :name: → emoji working set, small enough to browse.
// Several names may share an emoji; the shortest is displayed.
var Curated = map[string]string{
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

// ByName is the emoji a name stands for — curated, then Unicode's single glyphs, then
// its sequences — and false for a name none of them has.
func ByName(name string) (string, bool) {
	for _, set := range []map[string]string{Curated, Standard, Sequences} {
		if e, ok := set[name]; ok {
			return e, true
		}
	}
	return "", false
}
