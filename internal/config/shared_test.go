package config

import (
	"bytes"
	"strings"
	"testing"
)

// sharedSample spells a config the way a hand might, every kind of key in it: scalars,
// a list, lists of tables, an inline map with keys that need quotes, an emptied list
// that would otherwise take the starter's, and local keys beside shared ones.
const sharedSample = `
allow_token_file = true
tag = []

[display]
first_name_only = true
priority = ["tag:Work", "space:Home"]

[[display.space_rule]]
space = "tag:Work"
show_archived = true

[gui.theme]
accounts = { "matrix:@me:example.org" = "#112233", plain = "#445566" }

[notifications]
max_per_room = 3

[[notifications.rule]]
name = "mentions"
match = "mention"

[[profile]]
name = "work"

[storage]
messages_per_room = 500

[spell]
command = "/opt/hunspell"
flag_rare_words = true

[[matrix.account]]
name = "me"
homeserver = "https://example.org"
user = "@me:example.org"
`

func TestTheConfigReadsBackFromItsLeaves(t *testing.T) {
	cfg, err := Decode(sharedSample)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	leaves, err := Leaves(cfg)
	if err != nil {
		t.Fatalf("Leaves: %v", err)
	}
	for _, path := range []string{"tag", `gui.theme.accounts."matrix:@me:example.org"`, "display.space_rule", "notifications.rule", "spell.command", "matrix.account"} {
		if _, ok := leaves[path]; !ok {
			t.Errorf("no register %s among %v", path, keysOf(leaves))
		}
	}
	back, err := FromLeaves(leaves)
	if err != nil {
		t.Fatalf("FromLeaves: %v", err)
	}
	if got, want := Encode(back), Encode(cfg); got != want {
		t.Errorf("read back:\n%s\nwant:\n%s", got, want)
	}
	again, err := Leaves(back)
	if err != nil {
		t.Fatalf("Leaves again: %v", err)
	}
	for path, v := range leaves {
		if !bytes.Equal(again[path], v) {
			t.Errorf("%s: %q, then %q: a rewrite that changes nothing must write nothing", path, v, again[path])
		}
	}
}

func TestLocalKeysAreThisMachines(t *testing.T) {
	for path, local := range map[string]bool{
		"allow_token_file": true, "storage.messages_per_room": true, "spell.command": true,
		"matrix.account": true, "complete.model.port": true, "remote.listen": true,
		"spell.flag_rare_words": false, "display.first_name_only": false, "tag": false,
		"complete.model.budget": false, "storage_x": false, "notifications.rule": false,
	} {
		if IsLocal(path) != local {
			t.Errorf("IsLocal(%s) = %v, want %v", path, !local, local)
		}
	}
}

func TestAnUnsetKeyReadsAsItsDefault(t *testing.T) {
	cfg, err := FromLeaves(map[string][]byte{"display.first_name_only": Unset, "tag": []byte("value = []\n")})
	if err != nil {
		t.Fatalf("FromLeaves: %v", err)
	}
	if cfg.Display.FirstNameOnly || cfg.Tags != nil {
		t.Errorf("got first_name_only %v, tags %v: want the default and none", cfg.Display.FirstNameOnly, cfg.Tags)
	}
	if got := LeafText(Unset); got != "unset" {
		t.Errorf("LeafText(Unset) = %q", got)
	}
	if _, err := FromLeaves(map[string][]byte{"display": []byte("value = 1\n"), "display.fps": []byte("value = 2\n")}); err == nil {
		t.Error("a key both a value and a table read as a config")
	}
	if _, err := FromLeaves(map[string][]byte{"display.no_such_key": []byte("value = 1\n")}); err == nil || !strings.Contains(err.Error(), "unknown keys") {
		t.Errorf("an unknown key: %v, want it refused", err)
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
