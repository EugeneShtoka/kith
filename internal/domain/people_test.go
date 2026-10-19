package domain

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"testing"
)

// Precedence: alias, network name, phone book name for a number, the number; an ID's
// bare digits are never a name.
func TestPeopleAreNamedByWhatThePersonKnows(t *testing.T) {
	t.Parallel()
	p := People{
		Alias: func(user string) string { return map[string]string{"telegram:7": "Vanya"}[user] },
		Dir:   NewDirectory([]PersonName{{Source: "phone", ID: PhoneID("15550100001"), Name: "Dana Levi", Rank: RankSaved}}, nil),
	}
	for _, tc := range []struct {
		user  string
		known []string
		want  string
	}{
		{"telegram:7", []string{"Ivan Petrov"}, "Vanya"},
		{"telegram:8", []string{"", "Olga"}, "Olga"},
		{"whatsapp:15550100001@s.whatsapp.net", []string{"+1 555 010 0001"}, "Dana Levi"},
		{"whatsapp:15550100001@s.whatsapp.net", nil, "Dana Levi"},
		{"whatsapp:15550100002@s.whatsapp.net", []string{"+15550100002"}, "+15550100002"},
		{"whatsapp:15550100002@s.whatsapp.net", []string{"+15550100002", "Eli"}, "Eli"},
		{"whatsapp:100000000000005@lid", []string{"100000000000005"}, "whatsapp:100000000000005@lid"},
	} {
		if got := p.Name(tc.user, tc.known...); got != tc.want {
			t.Errorf("Name(%s, %q) = %q, want %q", tc.user, tc.known, got, tc.want)
		}
	}
}

// Each mention is rewritten once at its own place: no double rewrite, repeated words
// are separate mentions, "@" stays, unplaceable mentions are left alone.
func TestMentionsAreRewrittenInOnePass(t *testing.T) {
	t.Parallel()
	names := map[string]string{"a": "Bo Bo", "b": "Cy", "c": "Dana", "d": ""}
	var asked []string
	name := func(m Mention, words string) string {
		asked = append(asked, m.UserID+"="+words)
		return names[m.UserID]
	}
	body := "@123456 and Bo and Bo, ask Ana and @Zed; #general"
	mentions := []Mention{
		{UserID: "a", Name: "@123456"}, // WhatsApp's digits: no words given
		{UserID: "b", Name: "Bo"},      // "Bo Bo" above must not become "Cy Cy"
		{UserID: "c", Name: "Bo"},      // the second Bo
		{UserID: "d", Name: "Ana"},     // no better name: left as written
		{UserID: "a", Name: "@Missing"},
		{RoomID: "!g:x", Name: "#general"},
	}
	got, drawn := ResolveMentions(body, mentions, name)
	if want := "@Bo Bo and Cy and Dana, ask Ana and @Zed; #general"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if drawn[0].Name != "@Bo Bo" || drawn[1].Name != "Cy" || drawn[2].Name != "Dana" || drawn[3].Name != "Ana" {
		t.Errorf("drawn = %+v, want each mention naming what the body now says", drawn)
	}
	if asked[0] != "a=" {
		t.Errorf("asked %q for WhatsApp's digits, want no words", asked[0])
	}
	if mentions[0].Name != "@123456" {
		t.Error("the mentions given were changed")
	}
}

// You are never hidden: not your own IDs, nor an ID an identity group puts with them,
// even when a link joins you to a marked person.
func TestHiddenPeopleNeverHideYou(t *testing.T) {
	t.Parallel()

	dir := NewDirectory(nil, []PersonLink{{Source: "book", ID: "@me:x", Other: "@spam:x"}})
	h := NewHiddenPeople([]string{"@spam:x"}, [][]string{{"@me:x", "telegram:1"}}, dir, []string{"@me:x"})
	for id, want := range map[string]bool{"@spam:x": true, "@me:x": false, "telegram:1": false, "@ann:x": false} {
		if got := h.Has(id); got != want {
			t.Errorf("Has(%s) = %v, want %v", id, got, want)
		}
	}
	if !NewHiddenPeople(nil, nil, dir, nil).Empty() {
		t.Error("nobody marked is not empty")
	}
}

// A person's IDs are exactly the IDs Has counts as them: what lists everything they
// said must agree with what hides them. Directories and identity groups are generated,
// chains through both included, and every listed ID asked about.
func TestAPersonsIDsAreTheOnesHasFinds(t *testing.T) {
	t.Parallel()
	ids := []string{"@a:x", "@b:x", "@c:x", "whatsapp:15550100001", "telegram:7", "slack:T1/U2",
		PhoneID("15550100001"), "signal:15550100002", PhoneID("15550100002"), "@d:x"}
	for seed := range uint64(300) {
		rng := rand.New(rand.NewPCG(seed, 11))
		pick := func() string { return ids[rng.IntN(len(ids))] }
		var links []PersonLink
		for range rng.IntN(5) {
			links = append(links, PersonLink{Source: "book", ID: pick(), Other: pick()})
		}
		var names []PersonName
		for range rng.IntN(3) {
			names = append(names, PersonName{Source: "book", ID: pick(), Name: "Dana", Rank: RankSaved})
		}
		dir := NewDirectory(names, links)
		// Identity groups as [[display.identity]] makes them: a key over several IDs.
		groups := map[string][]string{}
		key := map[string]string{}
		for range rng.IntN(4) {
			k := fmt.Sprint("g", rng.IntN(2))
			id := pick()
			if _, taken := key[id]; !taken {
				key[id] = k
				groups[k] = append(groups[k], id)
			}
		}
		same := func(id string) []string {
			out := dir.Identifiers(id)
			if k, ok := key[id]; ok {
				out = append(slices.Clone(out), groups[k]...)
			}
			return out
		}
		known := slices.Sorted(maps.Keys(key))
		marked := NewMarkedPeople([]string{pick()}, known, same)
		listed := marked.IDs()
		// Every ID the directory or a group lists, and the marked one.
		for _, id := range ids {
			if _, inDir := dir.personOf(id); !inDir && key[id] == "" && !slices.Contains(listed, id) {
				continue
			}
			if has, in := marked.Has(id), slices.Contains(listed, id); has != in {
				t.Fatalf("seed %d: Has(%s) = %v but IDs() = %v (links %v, groups %v)", seed, id, has, listed, links, groups)
			}
		}
	}
}
