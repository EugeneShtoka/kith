package daemon

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The bridge's config set one property at a time, for a client given neither the
// config's text nor a way to write it whole (the desktop): ConfigSettings lists what it
// may set, SetConfigSetting sets one, on the file as it is at the revision the client
// read, checked and written as UpdateConfig writes. A property is one register of the
// mesh's shared config (design-mesh.md, Config), a list whole, so the mesh's step 5 can
// carry the same writes.
//
// What a client may set is settable, by path, and nothing else: every other property
// of the bridge's file is excluded, by a group saying why
// (TestEveryBridgePropertyIsClassified holds every property to one or the other, so a
// new one is settable only once someone says so here). The TUI, the CLI and the file
// keep setting everything.

// setting is how one settable property is offered: under which of the TUI's settings
// groups, by what name, and what its value may be.
type setting struct {
	group, label string
	// choices are a text's accepted values; none, it takes any text.
	choices []choice
	// duration marks a text that is a length of time (time.ParseDuration's).
	duration bool
	// least and most bound a number; nil leaves it unbounded that way.
	least, most *int64
}

type choice struct{ value, detail string }

// choicesOf are values, each with what details says of it.
func choicesOf(values []string, details map[string]string) []choice {
	out := make([]choice, 0, len(values))
	for _, v := range values {
		out = append(out, choice{v, details[v]})
	}
	return out
}

// timeChoices are the time styles, each with how 14:30 looks in it.
func timeChoices() []choice {
	styles := domain.TimeStyles()
	out := make([]choice, 0, len(styles))
	for _, s := range styles {
		out = append(out, choice{s[0], s[1]})
	}
	return out
}

var deletedChoices = choicesOf(config.DeletedModes(), map[string]string{
	config.DeletedShow: `a "(deleted)" placeholder where it was`,
	config.DeletedHide: "the row taken out",
})

// settable is every property a client may set one at a time, by path.
var settable = map[string]setting{
	// Notifications: how many popups one room may raise, the rest summed up.
	"notifications.max_per_room": {group: "notifications", label: "Popups one room may raise", least: new(int64(0))},
	"notifications.rate_window":  {group: "notifications", label: "Within", duration: true},

	"display.room_name_rules": {group: "names", label: "Name rules apply to room names"},

	// Messages.
	"display.unread": {group: "display", label: "Unread badges count", choices: choicesOf(config.UnreadSources(), map[string]string{
		config.UnreadMessages:      "what you have not read",
		config.UnreadNotifications: "what your push rules would have notified you about",
	})},
	"display.deleted.mine":   {group: "display", label: "Your deleted messages", choices: deletedChoices},
	"display.deleted.others": {group: "display", label: "Others' deleted messages", choices: deletedChoices},
	"display.deleted.keep":   {group: "display", label: "Keep what a deleted message said"},
	"display.tracked.words":  {group: "display", label: "Tracked words"},
	"display.tracked.notify": {group: "display", label: "A tracked word notifies"},

	"display.rail.network_colors": {group: "rooms", label: "Rows in their network's color"},

	"display.time_format":       {group: "look", label: "Time format", choices: timeChoices()},
	"display.long_date_format":  {group: "look", label: "Date heading each day"},
	"display.short_date_format": {group: "look", label: "Date in lists"},

	"spell.enabled":         {group: "spelling", label: "Check spelling"},
	"spell.dictionaries":    {group: "spelling", label: "Dictionaries"},
	"spell.flag_rare_words": {group: "spelling", label: "Flag rare words"},
	"spell.rare_ratio":      {group: "spelling", label: "How much commoner the neighbor", least: new(int64(1))},

	// The completion model on this machine: nothing it reads leaves it.
	"complete.model.enabled":      {group: "completion", label: "Completion model"},
	"complete.model.context":      {group: "completion", label: "Token window", least: new(int64(0))},
	"complete.model.idle":         {group: "completion", label: "Stopped after idle for", duration: true},
	"complete.model.startup":      {group: "completion", label: "Starting may take", duration: true},
	"complete.model.timeout":      {group: "completion", label: "A prediction may take", duration: true},
	"complete.model.options":      {group: "completion", label: "Words offered at once", least: new(int64(1)), most: new(int64(domain.MaxCompletionOptions))},
	"complete.model.budget":       {group: "completion", label: "Context tokens", least: new(int64(0))},
	"complete.model.recent":       {group: "completion", label: "Recent messages read", least: new(int64(0))},
	"complete.model.gap":          {group: "completion", label: "Context stops at a silence of", duration: true},
	"complete.model.before_reply": {group: "completion", label: "Messages before a reply", least: new(int64(0))},

	"codes.min_length":    {group: "codes", label: "Shortest code", least: new(int64(1))},
	"codes.max_length":    {group: "codes", label: "Longest code", least: new(int64(1))},
	"codes.letters":       {group: "codes", label: "Codes may contain letters"},
	"codes.digits":        {group: "codes", label: "Codes may contain digits"},
	"codes.symbols":       {group: "codes", label: "Other characters in a code"},
	"codes.require_digit": {group: "codes", label: "Codes must contain a digit"},

	"schedule.overdue_cutoff_hours": {group: "advanced", label: "A late scheduled message is still sent within (hours)", least: new(int64(0))},
}

// exclusion is a group of properties no client sets one at a time, and why.
type exclusion struct{ prefix, why string }

// excluded are the bridge's properties left to the TUI, the CLI and the file, each
// group by its prefix. What a renderer holds an XSS holds, so none of these is one call
// away from a window showing other people's messages.
var excluded = append([]exclusion{
	{"allow_token_file", "lets kithd keep its keys in a file instead of the keyring"},
	{"bridge_contacts", "addresses kithd calls (bridges' provisioning URLs)"},
	{"remote.", "the addresses kithd listens on"},
	{"log.", "kithd's own log: what it holds and where it goes"},
	{"backup.", "this machine's backups: their folder, their key, and whether and how often they are made"},
	{"phonebook.", "a folder on this machine"},
	{"commands.", "a folder of programs to run"},
	{"spell.command", "a program kithd runs"},
	{"complete.model.command", "a program kithd runs"},
	{"complete.model.model", "a file kithd loads into that program"},
	{"complete.model.threads", "how much of this machine that program takes"},
	{"complete.model.port", "a port kithd listens on"},
	{"assist.", "what of your conversations is sent to a remote model: where, with which key, from which rooms, how much of them, and under which instructions"},
	{"agent.", "what an assistant (kith-mcp) may read and write"},
	{"clipboard.auto_copy", "copies one-time codes as they arrive, through each device's own clipboard command, which kithd cannot check"},
	{"codes.include", "places, edited whole: a profile's window would be shown, and could drop, other profiles' rooms (Places settings)"},
	{"codes.exclude", "places, edited whole (as codes.include)"},
	{"display.priority", "places, edited whole (as codes.include)"},
	{"display.rail.order", "places, edited whole (as codes.include)"},
	{"display.rail.hidden", "places, edited whole (as codes.include)"},
	{"display.rail.hide_when_empty", "places, edited whole (as codes.include)"},
}, networkExclusions()...)

// networkExclusions are the networks' sections: each bridge's own accounts, their
// default and their archive (design-mesh.md decision 1), which logins write.
func networkExclusions() []exclusion {
	var out []exclusion
	for _, n := range (config.Config{}).Networks() {
		section, _, _ := strings.Cut(n.Table(), ".")
		out = append(out, exclusion{section + ".", "a network's accounts, the one it sends from, and its archive: this bridge's own"})
	}
	return out
}

// settableProperty is the property at path and how it is offered; false when no client
// may set it this way.
func settableProperty(path string) (config.Property, setting, bool) {
	s, ok := settable[path]
	if !ok {
		return config.Property{}, setting{}, false
	}
	at := slices.IndexFunc(config.Properties(), func(p config.Property) bool {
		return p.File == config.BridgeFile && p.Path == path
	})
	if at < 0 {
		return config.Property{}, setting{}, false
	}
	return config.Properties()[at], s, true
}

// kind is how a client edits p; unspecified for a kind the call does not carry.
func (s setting) kind(p config.Property) v1.ConfigSettingKind {
	switch p.Kind {
	case config.PropertyBool, config.PropertyOptionalBool:
		return v1.ConfigSettingKind_CONFIG_SETTING_KIND_TOGGLE
	case config.PropertyInt, config.PropertyOptionalInt:
		return v1.ConfigSettingKind_CONFIG_SETTING_KIND_NUMBER
	case config.PropertyText:
		switch {
		case len(s.choices) > 0:
			return v1.ConfigSettingKind_CONFIG_SETTING_KIND_CHOICE
		case s.duration:
			return v1.ConfigSettingKind_CONFIG_SETTING_KIND_DURATION
		}
		return v1.ConfigSettingKind_CONFIG_SETTING_KIND_TEXT
	case config.PropertyList:
		return v1.ConfigSettingKind_CONFIG_SETTING_KIND_LIST
	case config.PropertyFloat, config.PropertyOptionalFloat:
	}
	return v1.ConfigSettingKind_CONFIG_SETTING_KIND_UNSPECIFIED
}

// served is p as cfg holds it, offered as s.
func (s setting) served(p config.Property, cfg config.Config) *v1.ConfigSetting {
	out := &v1.ConfigSetting{
		Path: p.Path, Group: s.group, Label: s.label, Kind: s.kind(p), Least: s.least, Most: s.most, Doc: p.Doc,
	}
	for _, c := range s.choices {
		out.Choices = append(out.Choices, &v1.ConfigChoice{Value: c.value, Detail: c.detail})
	}
	if d, ok := fallbackOf(p); ok {
		out.Fallback = configValue(d)
	}
	if v, set, _ := cfg.PropertyValue(p.Path); set {
		out.Value = configValue(v)
	}
	return out
}

// fallbackOf is the value in force while the file sets none: default.toml's, an
// optional's even where it is written only commented out, as the TUI's settings screen
// reads it ("# room_name_rules = true": unset is on).
func fallbackOf(p config.Property) (any, bool) {
	if p.Kind == config.PropertyOptionalBool || p.Kind == config.PropertyOptionalInt {
		p.Example = false
	}
	return p.DefaultValue()
}

// configValue is a property's typed value on the wire.
func configValue(v any) *v1.ConfigValue {
	switch x := v.(type) {
	case bool:
		return &v1.ConfigValue{Value: &v1.ConfigValue_Flag{Flag: x}}
	case int:
		return &v1.ConfigValue{Value: &v1.ConfigValue_Number{Number: int64(x)}}
	case string:
		return &v1.ConfigValue{Value: &v1.ConfigValue_Text{Text: x}}
	case []string:
		return &v1.ConfigValue{Value: &v1.ConfigValue_List{List: &v1.ConfigList{Entries: x}}}
	}
	return nil
}

// errValue is a value a setting does not take.
var errValue = errors.New("daemon: not a value this setting takes")

// typed is the value a client sent for p as config.SetProperty takes it, nil to clear
// it; one of another kind, out of bounds, not among the choices, or not a length of
// time is refused.
func (s setting) typed(p config.Property, sent *v1.ConfigValue) (any, error) {
	if sent.GetValue() == nil {
		return nil, nil
	}
	kind := s.kind(p)
	switch v := sent.GetValue().(type) {
	case *v1.ConfigValue_Flag:
		if kind == v1.ConfigSettingKind_CONFIG_SETTING_KIND_TOGGLE {
			return v.Flag, nil
		}
	case *v1.ConfigValue_Number:
		if kind == v1.ConfigSettingKind_CONFIG_SETTING_KIND_NUMBER {
			return s.number(p.Path, v.Number)
		}
	case *v1.ConfigValue_Text:
		switch kind {
		case v1.ConfigSettingKind_CONFIG_SETTING_KIND_CHOICE:
			if !slices.ContainsFunc(s.choices, func(c choice) bool { return c.value == v.Text }) {
				return nil, fmt.Errorf("%w: %s is one of %s, not %q", errValue, p.Path, s.choiceWords(), v.Text)
			}
			return v.Text, nil
		case v1.ConfigSettingKind_CONFIG_SETTING_KIND_DURATION:
			if d, err := time.ParseDuration(strings.TrimSpace(v.Text)); err != nil || d < 0 {
				return nil, fmt.Errorf("%w: %s is a length of time (90s, 10m, 1h30m), not %q", errValue, p.Path, v.Text)
			}
			return strings.TrimSpace(v.Text), nil
		case v1.ConfigSettingKind_CONFIG_SETTING_KIND_TEXT:
			return v.Text, nil
		default:
		}
	case *v1.ConfigValue_List:
		if kind == v1.ConfigSettingKind_CONFIG_SETTING_KIND_LIST {
			entries := v.List.GetEntries()
			if slices.ContainsFunc(entries, func(e string) bool { return strings.TrimSpace(e) == "" }) {
				return nil, fmt.Errorf("%w: an entry of %s is empty", errValue, p.Path)
			}
			return slices.Clone(entries), nil
		}
	}
	return nil, fmt.Errorf("%w: %s takes %s", errValue, p.Path, kindWords[kind])
}

// number is n if it is within s's bounds and an int holds it.
func (s setting) number(path string, n int64) (any, error) {
	if (s.least != nil && n < *s.least) || (s.most != nil && n > *s.most) || int64(int(n)) != n {
		return nil, fmt.Errorf("%w: %s is a whole number%s, not %d", errValue, path, s.boundWords(), n)
	}
	return int(n), nil
}

func (s setting) boundWords() string {
	switch {
	case s.least != nil && s.most != nil:
		return fmt.Sprintf(" from %d to %d", *s.least, *s.most)
	case s.least != nil:
		return fmt.Sprintf(" of at least %d", *s.least)
	case s.most != nil:
		return fmt.Sprintf(" of at most %d", *s.most)
	}
	return ""
}

func (s setting) choiceWords() string {
	values := make([]string, 0, len(s.choices))
	for _, c := range s.choices {
		values = append(values, c.value)
	}
	return strings.Join(values, ", ")
}

var kindWords = map[v1.ConfigSettingKind]string{
	v1.ConfigSettingKind_CONFIG_SETTING_KIND_TOGGLE:   "on or off",
	v1.ConfigSettingKind_CONFIG_SETTING_KIND_CHOICE:   "one of its choices",
	v1.ConfigSettingKind_CONFIG_SETTING_KIND_NUMBER:   "a whole number",
	v1.ConfigSettingKind_CONFIG_SETTING_KIND_DURATION: "a length of time",
	v1.ConfigSettingKind_CONFIG_SETTING_KIND_TEXT:     "text",
	v1.ConfigSettingKind_CONFIG_SETTING_KIND_LIST:     "a list",
}

// ConfigSettings is every setting a client may set one at a time, as the file holds it
// now, and the file's revision. The config is the bridge's: every profile reads the same.
func (s *server) ConfigSettings(context.Context, *req[v1.ConfigSettingsRequest]) (*resp[v1.ConfigSettingsResponse], error) {
	snap, err := s.Config.Read()
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	out := &v1.ConfigSettingsResponse{Revision: snap.Revision}
	for _, p := range config.Properties() {
		if st, ok := settable[p.Path]; ok && p.File == config.BridgeFile {
			out.Settings = append(out.Settings, st.served(p, snap.Config))
		}
	}
	return connect.NewResponse(out), nil
}

// SetConfigSetting sets or clears one setting ConfigSettings lists, on the file as it is
// at the request's base revision, checked and written as UpdateConfig writes a config.
// A value that changes nothing writes nothing.
func (s *server) SetConfigSetting(ctx context.Context, r *req[v1.SetConfigSettingRequest]) (*resp[v1.SetConfigSettingResponse], error) {
	path := r.Msg.GetPath()
	p, st, ok := settableProperty(path)
	if !ok {
		return nil, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("daemon: %q is not a setting set one at a time here; the terminal client, the CLI or the file sets it", path))
	}
	value, err := st.typed(p, r.Msg.GetValue())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	snap, err := s.Config.Read()
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if snap.Revision != r.Msg.GetBaseRevision() {
		return nil, configWriteErr(api.ErrConfigMoved)
	}
	cfg := snap.Config.Clone()
	if err = cfg.SetProperty(path, value); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	was, wasSet, _ := snap.Config.PropertyValue(path)
	now, nowSet, _ := cfg.PropertyValue(path)
	if wasSet == nowSet && reflect.DeepEqual(was, now) {
		return connect.NewResponse(&v1.SetConfigSettingResponse{Revision: snap.Revision, Setting: st.served(p, snap.Config)}), nil
	}
	written, err := s.Config.Write(ctx, snap.Revision, cfg, s.Daemon.CheckConfig, s.Reload)
	if err != nil {
		return nil, configWriteErr(err)
	}
	s.Streams.Configured(written)
	return connect.NewResponse(&v1.SetConfigSettingResponse{Revision: written.Revision, Setting: st.served(p, written.Config)}), nil
}
