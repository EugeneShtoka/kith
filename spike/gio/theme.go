package main

import (
	"image/color"
	"strconv"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
)

// palette is the desktop's theme: [gui.theme]'s roles for the mode, the device's over
// the shared config's, each unset one the desktop's built-in (styles.css). The CSS
// variable each stands for is in desktop/src/renderer/theme.ts.
type palette struct {
	accent, background, surface, text, muted, link, selection, unread, mention color.NRGBA
	bar, raised, border, unreadText, mentionText, focus, focusText             color.NRGBA
	hues                                                                       []color.NRGBA // --hue-N, initials' faces
	pictures                                                                   bool          // faces = "pictures"
}

// builtIn is styles.css's dark and light values, by role.
var builtIn = map[string]map[string]string{
	"dark": {
		"accent": "#4c8dff", "background": "#1e1f22", "surface": "#232428", "text": "#ececef",
		"muted": "#9ea1a8", "link": "#82aaff", "selection": "#283a5c", "unread": "#4c8dff",
		"mention": "#ff5f5f", "bar": "#1a1b1e", "raised": "#2b2d31", "border": "#3a3c42",
		"unread_text": "#ffffff", "mention_text": "#ffffff", "focus": "#4c8dff", "focus_text": "#ffffff",
	},
	"light": {
		"accent": "#1f6feb", "background": "#ffffff", "surface": "#f4f5f7", "text": "#1d1d1f",
		"muted": "#6b6f78", "link": "#0b5cd5", "selection": "#dbe7fb", "unread": "#1f6feb",
		"mention": "#d1242f", "bar": "#e8eaee", "raised": "#ffffff", "border": "#dcdfe4",
		"unread_text": "#ffffff", "mention_text": "#ffffff", "focus": "#1f6feb", "focus_text": "#ffffff",
	},
}

var builtInHues = map[string][]string{
	"dark":  {"#6ea0ff", "#34d399", "#fbbf24", "#a78bfa", "#f472b6", "#22d3ee", "#a3e635", "#e879f9"},
	"light": {"#1d4ed8", "#047857", "#b45309", "#7c3aed", "#be185d", "#0e7490", "#4d7c0f", "#a21caf"},
}

// themeOf resolves the palette. shortcut: "system" mode is dark, since Gio does not
// say what the desktop's scheme is; read the portal's color-scheme to follow it.
func themeOf(cfg config.Config, dev config.Device) palette {
	get := func(path string) string {
		if v, ok := dev.GUI.Theme.Get(path); ok && v != "" {
			return v
		}
		v, _ := cfg.GUI.Theme.Get(path)
		return v
	}
	mode := get("mode")
	if mode != "light" {
		mode = "dark"
	}
	role := func(name string) color.NRGBA {
		if c, ok := hexColor(get(mode + "." + name)); ok {
			return c
		}
		c, _ := hexColor(builtIn[mode][name])
		return c
	}
	p := palette{
		accent: role("accent"), background: role("background"), surface: role("surface"),
		text: role("text"), muted: role("muted"), link: role("link"), selection: role("selection"),
		unread: role("unread"), mention: role("mention"), bar: role("bar"), raised: role("raised"),
		border: role("border"), unreadText: role("unread_text"), mentionText: role("mention_text"),
		focus: role("focus"), focusText: role("focus_text"),
		pictures: get("faces") != "initials",
	}
	for _, h := range builtInHues[mode] {
		c, _ := hexColor(h)
		p.hues = append(p.hues, c)
	}
	return p
}

// hexColor reads "#rrggbb".
func hexColor(s string) (color.NRGBA, bool) {
	s = strings.TrimSpace(s)
	if len(s) != 7 || s[0] != '#' {
		return color.NRGBA{}, false
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return color.NRGBA{}, false
	}
	return rgb(uint32(v)), true
}
