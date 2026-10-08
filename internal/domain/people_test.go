package domain

import (
	"testing"
)

// A person is named by what this person told kith first: their alias, then a name
// the network gave, then the phone book's name for a number, then the number; an
// ID's bare digits are never a name.
func TestPeopleAreNamedByWhatThePersonKnows(t *testing.T) {
	t.Parallel()
	p := People{
		Alias: func(user string) string { return map[string]string{"telegram:7": "Vanya"}[user] },
		Book:  PhoneBook{"15550100001": "Dana Levi"},
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

// Every mention is rewritten at its own place, once: a name that contains another
// mention's words does not get rewritten again, the same words twice are two
// mentions, the "@" a mention had stays, and what cannot be placed is left alone.
func TestMentionsAreRewrittenInOnePass(t *testing.T) {
	t.Parallel()
	names := map[string]string{"a": "Bo Bo", "b": "Cy", "c": "Dana", "d": ""}
	var asked []string
	name := func(user, words string) string {
		asked = append(asked, user+"="+words)
		return names[user]
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
