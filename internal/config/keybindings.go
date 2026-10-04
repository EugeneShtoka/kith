package config

import (
	"fmt"
	"reflect"
	"slices"
)

// Jump is one chord and where it goes.
type Jump struct {
	Chord  string `toml:"chord"`
	Target string `toml:"target"`
}

// Jumps is the list of jump bindings.
type Jumps []Jump

// Validate refuses a chord bound twice.
func (j Jumps) Validate() error {
	seen := make(map[string]string, len(j))
	for _, entry := range j {
		if entry.Chord == "" {
			return fmt.Errorf("keys.jump: a binding with no chord, going to %q", entry.Target)
		}
		if first, dup := seen[entry.Chord]; dup {
			return fmt.Errorf("keys.jump: %q is bound twice — to %q and to %q; a chord goes to one place",
				entry.Chord, first, entry.Target)
		}
		seen[entry.Chord] = entry.Target
	}
	return nil
}

// Map is the bindings as the keymap wants them.
func (j Jumps) Map() map[string]string {
	out := make(map[string]string, len(j))
	for _, entry := range j {
		if entry.Chord != "" && entry.Target != "" {
			out[entry.Chord] = entry.Target
		}
	}
	return out
}

// Keys are kith's configurable keybindings. Every value is a comma-separated list, so
// an action can answer to several keys ("k,up"). Defaults and descriptions live in
// keySections.
type Keys struct {
	Quit        string         `toml:"quit"`
	Interrupt   string         `toml:"interrupt"`
	Paste       string         `toml:"paste"`
	Help        string         `toml:"help"`
	Command     string         `toml:"command"`
	Settings    string         `toml:"settings"`
	Why         string         `toml:"why"`
	Redraw      string         `toml:"redraw"`
	FocusNext   string         `toml:"focus_next"`
	FocusPrev   string         `toml:"focus_prev"`
	ToggleDND   string         `toml:"toggle_dnd"`
	ToggleMute  string         `toml:"toggle_mute"`
	JumpTo      string         `toml:"jump_to"`
	JumpBack    string         `toml:"jump_back"`
	JumpForward string         `toml:"jump_forward"`
	Jump        Jumps          `toml:"jump"`
	Nav         NavKeys        `toml:"nav"`
	Rail        RailKeys       `toml:"rail"`
	Rooms       RoomsKeys      `toml:"rooms"`
	Sort        SortKeys       `toml:"sort"`
	Search      SearchKeys     `toml:"search"`
	Timeline    TimelineKeys   `toml:"timeline"`
	Insert      InsertKeys     `toml:"insert"`
	Edit        EditKeys       `toml:"edit"`
	Completion  CompletionKeys `toml:"completion"`
	Emoji       EmojiKeys      `toml:"emoji"`
	Composer    ComposerKeys   `toml:"composer"`
	Picker      PickerKeys     `toml:"picker"`
	React       ReactKeys      `toml:"react"`
	Player      PlayerKeys     `toml:"player"`
	Verify      VerifyKeys     `toml:"verify"`
	Confirm     ConfirmKeys    `toml:"confirm"`
	Prompt      PromptKeys     `toml:"prompt"`
	Spell       SpellKeys      `toml:"spell"`
}

// EditKeys edit text in every field that takes typing: the composer, prompts and
// filters.
type EditKeys struct {
	Left              string `toml:"left"`
	Right             string `toml:"right"`
	WordLeft          string `toml:"word_left"`
	WordRight         string `toml:"word_right"`
	Start             string `toml:"start"`
	End               string `toml:"end"`
	DeleteBack        string `toml:"delete_back"`
	DeleteForward     string `toml:"delete_forward"`
	DeleteWordBack    string `toml:"delete_word_back"`
	DeleteWordForward string `toml:"delete_word_forward"`
	DeleteToStart     string `toml:"delete_to_start"`
	DeleteToEnd       string `toml:"delete_to_end"`
	Undo              string `toml:"undo"`
	Redo              string `toml:"redo"`
	RowUp             string `toml:"row_up"`
	RowDown           string `toml:"row_down"`
}

// RoomsKeys are the room list's own actions.
type RoomsKeys struct {
	Join          string `toml:"join"`
	Leave         string `toml:"leave"`
	Accept        string `toml:"accept"`
	Reject        string `toml:"reject"`
	People        string `toml:"people"`
	ViewMedia     string `toml:"view_media"`
	Name          string `toml:"name"`
	NotifyRule    string `toml:"notify_rule"`
	MarkRead      string `toml:"mark_read"`
	New           string `toml:"new"`
	Invite        string `toml:"invite"`
	Unban         string `toml:"unban"`
	BindJump      string `toml:"bind_jump"`
	Spaces        string `toml:"spaces"`
	MarkUnread    string `toml:"mark_unread"`
	Direction     string `toml:"direction"`
	Spam          string `toml:"spam"`
	GoReplacement string `toml:"go_replacement"`
	RenameThread  string `toml:"rename_thread"`
	ListThreads   string `toml:"list_threads"`
}

// RailKeys rearrange the spaces rail.
type RailKeys struct {
	BindJump   string `toml:"bind_jump"`
	Name       string `toml:"name"`
	MoveUp     string `toml:"move_up"`
	MoveDown   string `toml:"move_down"`
	Hide       string `toml:"hide"`
	ShowHidden string `toml:"show_hidden"`
	NotifyRule string `toml:"notify_rule"`
	MarkRead   string `toml:"mark_read"`
}

// SearchKeys open message search and drive its results. Search covers the local
// cache — every message the client has seen — so it is instant and works offline.
type SearchKeys struct {
	Room     string `toml:"room"`
	All      string `toml:"all"`
	Mentions string `toml:"mentions"`
	Files    string `toml:"files"`
	Jump     string `toml:"jump"`
	Scope    string `toml:"scope"`
	Close    string `toml:"close"`
	Star     string `toml:"star"`
}

// ConfirmKeys answer a destructive action's confirmation.
type ConfirmKeys struct {
	Yes string `toml:"yes"`
	No  string `toml:"no"`
}

// PromptKeys drive a one-line text prompt. Anything not bound here types itself,
// as in the composer.
type PromptKeys struct {
	Submit string `toml:"submit"`
	Cancel string `toml:"cancel"`
}

// NavKeys is cursor motion, consulted by every pane.
type NavKeys struct {
	Up           string `toml:"up"`
	Down         string `toml:"down"`
	Back         string `toml:"back"`
	Open         string `toml:"open"`
	PageUp       string `toml:"page_up"`
	PageDown     string `toml:"page_down"`
	HalfPageUp   string `toml:"half_page_up"`
	HalfPageDown string `toml:"half_page_down"`
	SelectOldest string `toml:"select_oldest"`
	SelectNewest string `toml:"select_newest"`
	ScrollOldest string `toml:"scroll_oldest"`
	ScrollNewest string `toml:"scroll_newest"`
}

// TimelineKeys are the message cursor's actions, active only in the timeline when
// you are not typing.
type TimelineKeys struct {
	Insert         string `toml:"insert"`
	Reply          string `toml:"reply"`
	GoReply        string `toml:"go_reply"`
	React          string `toml:"react"`
	Redact         string `toml:"redact"`
	Edit           string `toml:"edit"`
	Reveal         string `toml:"reveal"`
	NextMention    string `toml:"next_mention"`
	PrevMention    string `toml:"prev_mention"`
	NextAttachment string `toml:"next_attachment"`
	PrevAttachment string `toml:"prev_attachment"`
	PrevDay        string `toml:"prev_day"`
	NextDay        string `toml:"next_day"`
	Name           string `toml:"name"`
	NotifyRule     string `toml:"notify_rule"`
	CopyText       string `toml:"copy_text"`
	CopyURL        string `toml:"copy_url"`
	OpenURL        string `toml:"open_url"`
	OpenURLFocus   string `toml:"open_url_focus"`
	FollowLink     string `toml:"follow_link"`
	Topic          string `toml:"topic"`
	Star           string `toml:"star"`
	History        string `toml:"history"`
	CopyCode       string `toml:"copy_code"`
	Download       string `toml:"download"`
	SaveAs         string `toml:"save_as"`
	ViewMedia      string `toml:"view_media"`
	Play           string `toml:"play"`
	OpenThread     string `toml:"open_thread"`
	StartThread    string `toml:"start_thread"`
	ListThreads    string `toml:"list_threads"`
	RenameThread   string `toml:"rename_thread"`
}

// PlayerKeys drive the voice-note player.
type PlayerKeys struct {
	PlayPause   string `toml:"play_pause"`
	Back        string `toml:"back"`
	Forward     string `toml:"forward"`
	Slower      string `toml:"slower"`
	Faster      string `toml:"faster"`
	NormalSpeed string `toml:"normal_speed"`
	SaveSpeed   string `toml:"save_speed"`
	Stop        string `toml:"stop"`
}

// InsertKeys apply while composing. Anything not bound here types itself, so keep
// these to keys that produce no text.
type InsertKeys struct {
	Send         string `toml:"send"`
	Newline      string `toml:"newline"`
	Cancel       string `toml:"cancel"`
	Attach       string `toml:"attach"`
	Complete     string `toml:"complete"`
	CompleteAll  string `toml:"complete_all"`
	ModelPreview string `toml:"model_preview"`
	// Choose1..Choose5 take the first to fifth option on offer (see [complete]).
	Choose1 string `toml:"choose_1"`
	Choose2 string `toml:"choose_2"`
	Choose3 string `toml:"choose_3"`
	Choose4 string `toml:"choose_4"`
	Choose5 string `toml:"choose_5"`
}

// CompletionKeys apply while the completion popup is open — the @-mention dropdown, and
// the other sources that will share it.
type CompletionKeys struct {
	Next    string `toml:"next"`
	Prev    string `toml:"prev"`
	Accept  string `toml:"accept"`
	Dismiss string `toml:"dismiss"`
}

// SpellKeys open the correction walk and drive it.
type SpellKeys struct {
	Open       string `toml:"open"`
	OpenInsert string `toml:"open_insert"`
	Skip       string `toml:"skip"`
	Add        string `toml:"add"`
	Ignore     string `toml:"ignore"`
	Stop       string `toml:"stop"`
	// Choose1..Choose9 take the first to ninth suggestion.
	Choose1 string `toml:"choose_1"`
	Choose2 string `toml:"choose_2"`
	Choose3 string `toml:"choose_3"`
	Choose4 string `toml:"choose_4"`
	Choose5 string `toml:"choose_5"`
	Choose6 string `toml:"choose_6"`
	Choose7 string `toml:"choose_7"`
	Choose8 string `toml:"choose_8"`
	Choose9 string `toml:"choose_9"`
}

// EmojiKeys open and drive the emoji browser — a grid of every emoji kith knows,
// filterable by typing.
type EmojiKeys struct {
	Open   string `toml:"open"`
	Accept string `toml:"accept"`
	Cancel string `toml:"cancel"`
}

// ComposerKeys apply to writing a message.
type ComposerKeys struct {
	ExternalEdit string `toml:"external_edit"`
	Cut          string `toml:"cut"`
}

// PickerKeys apply while a chooser overlay is open — the people list, the identity
// list, the color swatches, the link list.
type PickerKeys struct {
	Filter   string `toml:"filter"`
	Name     string `toml:"name"`
	Kick     string `toml:"kick"`
	Ban      string `toml:"ban"`
	Rename   string `toml:"rename"`
	Toggle   string `toml:"toggle"`
	Increase string `toml:"increase"`
	Decrease string `toml:"decrease"`
	Accept   string `toml:"accept"`
	Close    string `toml:"close"`
}

// SortKeys choose the room list's order.
type SortKeys struct {
	UnreadFirst   string `toml:"unread_first"`
	MentionsFirst string `toml:"mentions_first"`
	DraftsFirst   string `toml:"drafts_first"`
	Recent        string `toml:"recent"`
	Alphabetical  string `toml:"alphabetical"`
}

// ReactKeys apply while the emoji prompt is open. Digits stay positional over the
// quick palette and are not configurable — they *are* the palette's positions.
type ReactKeys struct {
	Send   string `toml:"send"`
	Cancel string `toml:"cancel"`
	// Pick1..Pick10 react with the palette's first to tenth emoji.
	Pick1  string `toml:"pick_1"`
	Pick2  string `toml:"pick_2"`
	Pick3  string `toml:"pick_3"`
	Pick4  string `toml:"pick_4"`
	Pick5  string `toml:"pick_5"`
	Pick6  string `toml:"pick_6"`
	Pick7  string `toml:"pick_7"`
	Pick8  string `toml:"pick_8"`
	Pick9  string `toml:"pick_9"`
	Pick10 string `toml:"pick_10"`
}

// VerifyKeys apply while the device-verification overlay is up, which captures
// every other key.
type VerifyKeys struct {
	Confirm string `toml:"confirm"`
	Cancel  string `toml:"cancel"`
}

// DefaultKeys are the built-in bindings — what kith does with no [keys] section
// at all. They come from keySections.
func DefaultKeys() Keys { return defaultKeys }

var defaultKeys = func() Keys {
	var k Keys
	v := reflect.ValueOf(&k).Elem()
	for _, section := range keySections {
		for _, b := range section.binds {
			index, ok := keyFields[b.path]
			if !ok {
				panic("config: keySections names " + b.path + ", which Keys has no field for")
			}
			v.FieldByIndex(index).SetString(b.def)
		}
	}
	return k
}()

// keyFields maps each binding's path ("timeline.reply") to its field in Keys.
var keyFields = func() map[string][]int {
	out := map[string][]int{}
	var walk func(t reflect.Type, prefix string, index []int)
	walk = func(t reflect.Type, prefix string, index []int) {
		for i := range t.NumField() {
			f := t.Field(i)
			at := append(slices.Clone(index), i)
			switch f.Type.Kind() {
			case reflect.String:
				out[prefix+tomlName(f)] = at
			case reflect.Struct:
				walk(f.Type, prefix+tomlName(f)+".", at)
			}
		}
	}
	walk(reflect.TypeFor[Keys](), "", nil)
	return out
}()

// Binding is the value of the binding at path ("timeline.reply"), and false for a path
// Keys has no field for.
func (k Keys) Binding(path string) (string, bool) {
	index, ok := keyFields[path]
	if !ok {
		return "", false
	}
	return reflect.ValueOf(k).FieldByIndex(index).String(), true
}

// KeyDoc is what the binding at path does, in one line, and false for an unknown path.
func KeyDoc(path string) (string, bool) {
	doc, ok := keyDocs[path]
	return doc, ok
}

var keyDocs = func() map[string]string {
	out := map[string]string{}
	for _, section := range keySections {
		for _, b := range section.binds {
			out[b.path] = b.doc
		}
	}
	return out
}()

// FillDefaults gives every binding the config file left out its built-in value.
func (k *Keys) FillDefaults() {
	fillStringDefaults(reflect.ValueOf(k).Elem(), reflect.ValueOf(DefaultKeys()))
}

// fillStringDefaults recursively copies defaults into empty string fields of target.
func fillStringDefaults(target, defaults reflect.Value) {
	for i := range target.NumField() {
		field, fallback := target.Field(i), defaults.Field(i)
		switch field.Kind() {
		case reflect.String:
			if field.String() == "" {
				field.Set(fallback)
			}
		case reflect.Struct:
			fillStringDefaults(field, fallback)
		}
	}
}
