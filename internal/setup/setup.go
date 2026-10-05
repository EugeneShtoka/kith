// Package setup turns a configuration into the structures a running program reads:
// notification rules, notifier sinks, the code detector's shape and scope.
package setup

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/EugeneShtoka/kith/internal/audio"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
	"github.com/EugeneShtoka/kith/internal/themespec"
)

// errOf drops a resolved value so a (value, error) check fits in Validate's list.
func errOf[T any](_ T, err error) error { return err }

// Validate checks every section whose failure is cheaper to find at startup than at
// the first message, and returns the first problem in the order listed.
func Validate(cfg config.Config) error {
	d := cfg.Display
	for _, err := range []error{
		PlaceEntries(cfg),
		Scripts(cfg),
		errOf(NotificationRules(cfg.Notifications)),
		errOf(CodeRules(cfg.Codes)),
		errOf(SpamRules(cfg.Spam)),
		errOf(SkinTone(d.SkinTone)),
		errOf(EmojiTier(d.Emoji.Set)),
		FrameRate(d.FPS),
		errOf(MediaMode(d.Media.Mode)),
		errOf(MediaDetail(d.Media.Detail)),
		errOf(UnreadSource(d.Unread)),
		RoomList(d.Rooms),
		themeCheck(d.Theme),
		errOf(ThreadListing(d.Threads.InRoomList)),
		AudioPlayback(d.Media),
		errOf(NotificationLimit(cfg.Notifications)),
		errOf(RareUnderline(cfg.Spell.RareUnderline)),
		errOf(AutocorrectMode(cfg.Spell.Autocorrect)),
		errOf(SpellUnderline(cfg.Spell.Underline)),
		DeletedMessages(d.Deleted),
		Completion(cfg.Complete),
		ModelLayer(cfg.Assist),
		errOf(CompletionPick(cfg.Complete.Model)),
		errOf(AgentCooldown(cfg.Agent)),
		errOf(LogLevel(cfg.Log)),
		errOf(LogTarget(cfg.Log)),
		tagsCheck(cfg),
	} {
		if err != nil {
			return err
		}
	}
	return nil
}

func themeCheck(t config.Theme) error {
	if _, err := themespec.Resolve(t.Preset, t.Overrides()); err != nil {
		return fmt.Errorf("display.theme: %w", err)
	}
	return nil
}

// DeletedMessages checks both halves of [display.deleted], naming which one is wrong.
func DeletedMessages(cfg config.Deleted) error {
	if _, err := oneOf("display.deleted.mine", "setting", cfg.Mine, config.DeletedModes()); err != nil {
		return err
	}
	_, err := oneOf("display.deleted.others", "setting", cfg.Others, config.DeletedModes())
	return err
}

// SpellUnderline resolves how a misspelling is marked in the composer.
func SpellUnderline(name string) (string, error) {
	return oneOf("spell.underline", "underline", name, config.SpellUnderlines())
}

// RareUnderline resolves how a rare word is marked.
func RareUnderline(name string) (string, error) {
	return oneOf("spell.rare_underline", "underline", name, config.RareUnderlines())
}

// Completion checks [complete]: vocabulary scope, sources, and the local model.
func Completion(cfg config.Complete) error {
	if _, err := oneOf("complete.scope", "scope", cfg.ScopeOrDefault(), config.CompleteScopes()); err != nil {
		return err
	}
	for _, source := range cfg.Sources {
		if _, err := oneOf("complete.sources", "source", source, config.CompleteSources()); err != nil {
			return err
		}
	}
	if err := ModelDurations(cfg.Model); err != nil {
		return err
	}
	return ModelNames(cfg.Model)
}

// AutocorrectMode resolves what autocorrect may rewrite; empty is "off".
func AutocorrectMode(mode config.Autocorrect) (string, error) {
	return oneOf("spell.autocorrect", "mode", string(mode), config.AutocorrectModes())
}

// AudioPlayback checks the voice-note speed (global and per rule) and skip step. A
// configured speed out of range is refused here, while the player clamps at runtime.
func AudioPlayback(cfg config.Media) error {
	if err := audioSpeed("display.media.audio.speed", cfg.Audio.Speed); err != nil {
		return err
	}
	for _, rule := range cfg.Rules {
		if rule.Speed == nil {
			continue
		}
		where := rule.Match
		if where == "" {
			where = rule.Sender
		}
		if err := audioSpeed(fmt.Sprintf("display.media.rule (%s): speed", where), *rule.Speed); err != nil {
			return err
		}
	}
	if skip := cfg.Audio.Skip; skip < 0 || skip > config.MaxAudioSkip {
		return fmt.Errorf("display.media.audio.skip: %d is out of range (want 1-%d seconds, or omit it for %d)",
			skip, config.MaxAudioSkip, config.DefaultAudioSkip)
	}
	return nil
}

// audioSpeed refuses a multiplier the player could not honor; zero means the default.
func audioSpeed(where string, speed float64) error {
	if speed == 0 {
		return nil
	}
	if speed < audio.MinSpeed || speed > audio.MaxSpeed {
		return fmt.Errorf("%s: %g is out of range (want %g-%g, or omit it for %g)",
			where, speed, audio.MinSpeed, audio.MaxSpeed, config.DefaultAudioSpeed)
	}
	return nil
}

// MediaMode resolves how images are drawn in the timeline.
func MediaMode(name string) (string, error) {
	return oneOf("display.media.mode", "mode", name, config.MediaModes())
}

// MediaDetail resolves how much of a picture one character carries.
func MediaDetail(name string) (string, error) {
	return oneOf("display.media.detail", "detail", name, config.MediaDetails())
}

// FrameRate checks a configured repaint rate; out of range is refused, not clamped.
func FrameRate(fps int) error {
	if fps == 0 { // unset: config.Display.FrameRate supplies the default
		return nil
	}
	if fps < config.MinFPS || fps > config.MaxFPS {
		return fmt.Errorf("display.fps: %d is out of range (want %d-%d, or omit it for %d)",
			fps, config.MinFPS, config.MaxFPS, config.DefaultFPS)
	}
	return nil
}

// EmojiTier resolves how much of the standard emoji set to offer.
func EmojiTier(name string) (string, error) {
	return oneOf("display.emoji.set", "set", name, config.EmojiTiers())
}

// SkinTone resolves a tone name to the modifier that applies it, or "" for none.
func SkinTone(name string) (string, error) {
	tone, err := oneOf("display.skin_tone", "tone", name, config.SkinToneNames())
	if err != nil {
		return "", err
	}
	modifier, _ := config.SkinToneModifier(tone) // oneOf already proved it is one
	return modifier, nil
}

// ThreadListing resolves which of a room's threads are listed beneath it.
func ThreadListing(name string) (string, error) {
	return oneOf("display.threads.in_room_list", "value", name, config.ThreadListings())
}

// UnreadSource resolves what a badge counts.
func UnreadSource(name string) (string, error) {
	return oneOf("display.unread", "source", name, config.UnreadSources())
}

// RoomList checks the room list's sort chain, globally and per group rule.
func RoomList(rooms config.Rooms) error {
	if err := sortChain("display.rooms.sort", rooms.Sort); err != nil {
		return err
	}
	for _, rule := range rooms.Rules {
		if strings.TrimSpace(rule.Group) == "" {
			return fmt.Errorf("display.rooms.rule: a rule needs a group " +
				"(a space's name, or home/dms/unread)")
		}
		if err := sortChain(fmt.Sprintf("display.rooms.rule %q.sort", rule.Group), rule.Sort); err != nil {
			return err
		}
	}
	return nil
}

// sortChain checks a chain of comparators, naming every unknown key at once.
func sortChain(where string, chain []string) error {
	if _, unknown := domain.ParseSortChain(chain); len(unknown) > 0 {
		return fmt.Errorf("%s: unknown sort %s (want %s)",
			where, quoteAll(unknown), strings.Join(domain.SortKeys(), ", "))
	}
	return nil
}

// quoteAll is a list of values for an error message, each quoted.
func quoteAll(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, fmt.Sprintf("%q", v))
	}
	return strings.Join(quoted, ", ")
}

// oneOf resolves a configured word against its vocabulary. section is what to edit
// ("display.emoji.set"), noun what the value is ("set").
func oneOf(section, noun, name string, allowed []string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	if trimmed == "" {
		return allowed[0], nil
	}
	for _, want := range allowed {
		if trimmed == want {
			return want, nil
		}
	}
	return "", fmt.Errorf("%s: unknown %s %q (want %s)",
		section, noun, name, humanList(allowed))
}

// humanList renders a vocabulary the way a sentence would: "a, b or c".
func humanList(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	default:
		return strings.Join(words[:len(words)-1], ", ") + " or " + words[len(words)-1]
	}
}

// LogTarget parses `[log] target`.
func LogTarget(l config.Log) (logging.Target, error) {
	target, err := logging.ParseTarget(l.Target)
	if err != nil {
		return target, fmt.Errorf("[log] target: %w", err)
	}
	return target, nil
}

// LogLevel parses `[log] level`.
func LogLevel(l config.Log) (slog.Level, error) {
	level, err := logging.ParseLevel(l.Level)
	if err != nil {
		return level, fmt.Errorf("[log] level: %w", err)
	}
	return level, nil
}
