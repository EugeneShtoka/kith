package protoconv

import (
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// Only temporary (do-not-disturb) rules travel: standing rules are in the config
// file both processes read.

// levels is written out so renumbering either enum is caught here.
var levels = map[notify.Level]v1.Level{
	notify.LevelNone:    v1.Level_LEVEL_NONE,
	notify.LevelMention: v1.Level_LEVEL_MENTION,
	notify.LevelDM:      v1.Level_LEVEL_DM,
	notify.LevelAll:     v1.Level_LEVEL_ALL,
}

// levelPtrToProto sends nil ("this rule leaves the axis alone") as UNSPECIFIED.
func levelPtrToProto(l *notify.Level) v1.Level {
	if l == nil {
		return v1.Level_LEVEL_UNSPECIFIED
	}
	if level, ok := levels[*l]; ok {
		return level
	}
	return v1.Level_LEVEL_UNSPECIFIED
}

// levelPtrFromProto reads UNSPECIFIED back as nil rather than as a level.
func levelPtrFromProto(l v1.Level) *notify.Level {
	if l == v1.Level_LEVEL_UNSPECIFIED {
		return nil
	}
	level := notify.LevelNone
	for lv, wire := range levels {
		if wire == l {
			level = lv
			break
		}
	}
	return &level
}

// TempRuleToProto converts one temporary rule; no deadline sends Until unset.
func TempRuleToProto(r notify.Rule) *v1.TempRule {
	return &v1.TempRule{
		Name:   r.Name,
		Match:  r.Match,
		Sender: r.Sender,
		Show:   levelPtrToProto(r.Show),
		Ring:   levelPtrToProto(r.Ring),
		Until:  toProtoTime(r.Until),
	}
}

// TempRulesToProto converts a set of rules, preserving order.
func TempRulesToProto(rs notify.Temps) []*v1.TempRule {
	return mapSlice(rs, TempRuleToProto)
}

// TempRuleFromProto converts one rule back. Temp is always set: only temporary
// rules cross this wire, and they must not outlive a restart.
func TempRuleFromProto(r *v1.TempRule) notify.Rule {
	if r == nil {
		return notify.Rule{Temp: true}
	}
	return notify.Rule{
		Name:   r.GetName(),
		Match:  r.GetMatch(),
		Sender: r.GetSender(),
		Show:   levelPtrFromProto(r.GetShow()),
		Ring:   levelPtrFromProto(r.GetRing()),
		Until:  fromProtoTime(r.GetUntil()),
		Temp:   true,
	}
}

// TempRulesFromProto converts a set of rules back.
func TempRulesFromProto(rs []*v1.TempRule) notify.Temps {
	return mapSlice(rs, TempRuleFromProto)
}
