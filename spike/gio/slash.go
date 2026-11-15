package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"gioui.org/widget"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The composer's `/` commands, as the TUI's and the desktop's (slash.ts): a known command
// is not a message, `//` escapes one slash, and words over more than one line are never
// a command. The window does the commands that send (/me, /plain, /at, /in, /poll) and
// those it has the action for (/upload, /search); the TUI's others say so. Typing `/` at
// the start of the words offers the window's commands.
// shortcut: the commands folder (kith-relay) is not read here, so any other slash word
// is refused rather than sent as words, which a folder command would otherwise be; read
// it (device.Commands) when the window is to run them.

// slashCommand is a command the window does: its name, the words it takes ("" none), and
// whether it runs without them.
type slashCommand struct {
	name, arg, summary string
	argOptional        bool
}

// windowCommands are the TUI's commands this window does, in the TUI's order.
var windowCommands = []slashCommand{
	{name: "/me", arg: "<what you are doing>", summary: "send as an emote"},
	{name: "/plain", arg: "<message>", summary: "send it exactly as typed, no Markdown"},
	{name: "/upload", summary: "send a file, picked in the open dialog"},
	{name: "/at", arg: "<time> <message>", summary: "send it later: /at 09:00 good morning"},
	{name: "/in", arg: "<delay> <message>", summary: "send it after a delay: /in 2h see you then"},
	{name: "/search", arg: "[terms]", argOptional: true, summary: "search this room"},
	{name: "/poll", arg: "<question> | <answer> | <answer> [| any]", summary: "ask a poll; a trailing | any lets voters pick several"},
}

// tuiCommands are the TUI's commands the window has not got (slash.ts tuiOnly and the
// desktop's own).
var tuiCommands = []string{
	"/summary", "/export", "/tracked", "/files", "/mentions", "/hidden", "/threads", "/starred",
	"/scheduled", "/ignored", "/discover", "/knock", "/topic", "/why", "/unread", "/tag", "/caught",
	"/direction", "/shortcut", "/replacement", "/invite", "/unban", "/leave",
}

// commandOf reads body as the TUI's submit does: the slash word and its words after a
// space or colon; ok false for a message.
func commandOf(body string) (name, arg string, ok bool) {
	if !strings.HasPrefix(body, "/") || strings.HasPrefix(body, "//") || len(body) == 1 || strings.Contains(body, "\n") {
		return "", "", false
	}
	for _, c := range windowCommands {
		if body == c.name {
			return c.name, "", true
		}
		for _, sep := range []string{" ", ":"} {
			if rest, found := strings.CutPrefix(body, c.name+sep); found {
				return c.name, strings.TrimSpace(rest), true
			}
		}
	}
	name, arg, _ = strings.Cut(body, " ")
	return name, strings.TrimSpace(arg), true
}

// command does the command in ed's words, if they are one; false when they are a message
// to send. A command it cannot do, or one missing its words, says so and keeps them.
func (u *ui) command(ctx context.Context, ed *widget.Editor, root domain.EventID, body string) bool {
	name, arg, ok := commandOf(body)
	if !ok {
		return false
	}
	tell := func(note string) bool {
		u.note, u.noteAt = note, time.Now()
		return true
	}
	i := slices.IndexFunc(windowCommands, func(c slashCommand) bool { return c.name == name })
	switch {
	case slices.Contains(tuiCommands, name):
		return tell(name + " is in the TUI; it is not in this window yet")
	case i < 0:
		return tell(name + " is not a command here; start with // to send it as words")
	case windowCommands[i].arg != "" && !windowCommands[i].argOptional && arg == "":
		return tell(name + " " + windowCommands[i].arg)
	}
	if note := u.doCommand(ctx, ed, root, name, arg); note != "" {
		return tell(note)
	}
	ed.SetText("")
	delete(u.mentions, ed)
	if ed == &u.composer {
		u.replyTo = ""
	}
	return true
}

// doCommand does command name with its words, answering why not where it cannot.
func (u *ui) doCommand(ctx context.Context, ed *widget.Editor, root domain.EventID, name, arg string) string {
	room, mentions := u.current, u.mentions[ed]
	var replyTo domain.EventID
	if ed == &u.composer {
		replyTo = u.replyTo
	}
	switch name {
	case "/me", "/plain":
		go u.send(ctx, room, domain.Draft{Body: arg, ThreadRoot: root, ReplyTo: replyTo, Mentions: mentions, Emote: name == "/me", Plain: name == "/plain"})
	case "/at", "/in":
		at, words, err := scheduleTime(name[1:], arg, time.Now())
		if err != nil {
			return name + " " + err.Error()
		}
		msg := domain.ScheduledMessage{
			RoomID: room, Body: words, ThreadRoot: root, ReplyTo: replyTo, Mentions: mentions,
			At: at.UTC(), Written: time.Now().UTC(),
		}
		go func() {
			if _, err := u.backend.Schedule(ctx, msg); err != nil {
				u.say("Not queued: " + err.Error())
				return
			}
			u.say("Queued for " + at.Format("Mon 3:04 PM"))
		}()
	case "/poll":
		question, options, multiple, ok := pollOf(arg)
		if !ok {
			return "/poll <question> | <answer1> | <answer2> [| …] [| any]"
		}
		go func() {
			if _, err := u.backend.CreatePoll(ctx, room, question, options, multiple); err != nil {
				u.say("Poll not asked: " + err.Error())
			}
		}()
	case "/upload":
		go u.attach(ctx, room)
	case "/search":
		u.searchScope = scopeRoom
		u.search.SetText(arg)
		if arg == "" {
			u.focus = &u.search
		} else {
			u.runSearch(ctx)
		}
	}
	return ""
}

// scheduleTime splits `/at 09:00 words` or `/in 2h words` into the time to send and the
// words (the TUI's parseScheduleTime).
func scheduleTime(kind, arg string, now time.Time) (time.Time, string, error) {
	when, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
	rest = strings.TrimSpace(rest)
	if rest == "" {
		example := "09:00 good morning"
		if kind == "in" {
			example = "2h see you then"
		}
		return time.Time{}, "", fmt.Errorf("needs a time and a message: try /%s %s", kind, example)
	}
	if kind == "in" {
		d, err := time.ParseDuration(when)
		switch {
		case err != nil:
			return time.Time{}, "", fmt.Errorf("%q is not a delay: try 30m, 2h, 1h30m", when)
		case d <= 0:
			return time.Time{}, "", fmt.Errorf("a delay has to be in the future: %q is not", when)
		}
		return now.Add(d), rest, nil
	}
	for _, layout := range []string{"15:04", "1504", "3pm", "3:04pm"} {
		t, err := time.Parse(layout, strings.ToLower(when))
		if err != nil {
			continue // not this layout; said below when none fits
		}
		at := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
		if !at.After(now) {
			at = at.AddDate(0, 0, 1) // past today: tomorrow
		}
		return at, rest, nil
	}
	return time.Time{}, "", fmt.Errorf("%q is not a time: try 09:00 or 9pm", when)
}

// pollOf reads `question | answer | answer [| …] [| any]` (the TUI's parsePollArg).
func pollOf(arg string) (question string, options []string, multiple, ok bool) {
	parts := strings.Split(arg, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	if len(parts) < 3 {
		return "", nil, false, false
	}
	question, options = parts[0], parts[1:]
	if strings.EqualFold(options[len(options)-1], "any") {
		multiple, options = true, options[:len(options)-1]
	}
	if question == "" || len(options) < 2 || slices.Contains(options, "") {
		return "", nil, false, false
	}
	return question, options, multiple, true
}

// commandOffers are the window's commands whose names start with what is typed: the
// words up to the caret, one slash word at the very start.
func commandOffers(before string) []candidate {
	if !strings.HasPrefix(before, "/") || strings.HasPrefix(before, "//") || strings.ContainsAny(before, " \t\n") {
		return nil
	}
	var offers []candidate
	for _, c := range windowCommands {
		if strings.HasPrefix(c.name, before) {
			label, put := c.name+"  "+c.summary, c.name
			if c.arg != "" {
				label, put = c.name+" "+c.arg+"  "+c.summary, c.name+" "
			}
			offers = append(offers, candidate{label: label, put: put})
		}
	}
	return offers
}
