package domain

import "testing"

// A label is a number only when it is nothing else: "+", digits and the punctuation
// numbers are written with, a bridge's tag allowed after it.
func TestALabelIsANumberOnlyWhenItIsNothingElse(t *testing.T) {
	t.Parallel()
	for label, want := range map[string]string{
		"+15550100123":            "15550100123",
		"+1 555-010-0123":         "15550100123",
		"+44 (20) 7946.0958":      "442079460958",
		"+15550100123 (WA)":       "15550100123",
		" +15550100123 ":          "15550100123",
		"15550100123":             "", // no "+": a name made of digits, or a local number
		"+1555":                   "", // too short to be anyone's number
		"+1555010012345678":       "", // too long
		"Dana +15550100123":       "",
		"+15550100123 (Work)":     "", // a person's own words after it, not a bridge's tag
		"+15550100123 (WA) extra": "",
		"":                        "",
	} {
		got, ok := PhoneIn(label)
		if got != want || ok != (want != "") {
			t.Errorf("PhoneIn(%q) = %q, %v; want %q", label, got, ok, want)
		}
	}
}

// A bridge's tag comes off a name; brackets that are part of the name stay.
func TestABridgesTagComesOffAName(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"Dana Levi (WA)":      "Dana Levi",
		"Dana (SIG)":          "Dana",
		"Dana (Internations)": "Dana (Internations)",
		"Dana (wa)":           "Dana (wa)",
		"Dana":                "Dana",
		"(WA)":                "(WA)",
	} {
		if got := WithoutBridgeTag(name); got != want {
			t.Errorf("WithoutBridgeTag(%q) = %q, want %q", name, got, want)
		}
	}
}

// A person's number is read from an ID made of one: WhatsApp's, directly or through
// a bridge of any instance. Another network's numeric ID is no phone number.
func TestAPersonsNumberIsReadFromTheirID(t *testing.T) {
	t.Parallel()
	for id, want := range map[string]string{
		"whatsapp:15550100123@s.whatsapp.net":     "15550100123",
		"whatsapp:15550100123@lid":                "",
		"whatsapp:120363000000000000@g.us":        "",
		"whatsapp:111/15550100123@s.whatsapp.net": "", // a room, not a person
		"@whatsapp_15550100123:example.org":       "15550100123",
		"@whatsapp_il_15550100123:example.org":    "15550100123",
		"@whatsapp_lid-abc:example.org":           "",
		"@whatsappbot:example.org":                "",
		"@telegram_123456789:example.org":         "",
		"@gmessages_2.610:example.org":            "",
		"@dana:example.org":                       "",
		"telegram:123456789":                      "",
	} {
		if got := PhoneOf(id); got != want {
			t.Errorf("PhoneOf(%q) = %q, want %q", id, got, want)
		}
	}
}

// The book names a label that is only a number it knows; any other label is not its.
func TestTheBookNamesOnlyANumberItKnows(t *testing.T) {
	t.Parallel()
	book := PhoneBook{"15550100123": "Dana"}
	for label, want := range map[string]string{
		"+1 555 010 0123":   "Dana",
		"+15550100123 (WA)": "Dana",
		"+15550100999":      "",
		"Dana":              "",
	} {
		got, ok := book.Named(label)
		if got != want || ok != (want != "") {
			t.Errorf("Named(%q) = %q, %v; want %q", label, got, ok, want)
		}
	}
}
