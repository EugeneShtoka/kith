package domain

import "testing"

func TestMessageLink(t *testing.T) {
	for _, tc := range []struct {
		name        string
		room, event string
		want        string
	}{
		{"matrix", "matrix:fedcba9876543210/!room:example.org", "matrix:fedcba9876543210/$ev/ent+1",
			"https://matrix.to/#/%21room:example.org/$ev%2Fent+1"},
		{"telegram channel", "telegram:1/-1001234", "telegram:1/-1001234/56", "https://t.me/c/1234/56"},
		{"telegram user", "telegram:1/777", "telegram:1/777/56", "kith:telegram:1%2F777?event=telegram%3A1%2F777%2F56"},
		{"telegram basic group", "telegram:1/-42", "telegram:1/-42/56", "kith:telegram:1%2F-42?event=telegram%3A1%2F-42%2F56"},
		{"telegram other chat's message", "telegram:1/-1001234", "telegram:1/-1009999/56",
			"kith:telegram:1%2F-1001234?event=telegram%3A1%2F-1009999%2F56"},
		{"slack", "slack:T1/C2", "slack:T1/C2/1700000000.000100",
			"kith:slack:T1%2FC2?event=slack%3AT1%2FC2%2F1700000000.000100"},
		{"other account's event", "matrix:fedcba9876543210/!room:example.org", "matrix:0123456789abcdef/$ev",
			"kith:matrix:fedcba9876543210%2F%21room:example.org?event=matrix%3A0123456789abcdef%2F%24ev"},
		{"no event", "matrix:fedcba9876543210/!room:example.org", "", "kith:matrix:fedcba9876543210%2F%21room:example.org"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := MessageLink(RoomID(tc.room), EventID(tc.event)); got != tc.want {
				t.Errorf("MessageLink(%q, %q) = %q, want %q", tc.room, tc.event, got, tc.want)
			}
		})
	}
}
