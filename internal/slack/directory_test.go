package slack

import (
	"context"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A Slack user learned from a message is in the directory by their ID, under the name
// they chose, and linked to the number their profile gives: a name an address book on
// another network saved for that number names them on Slack too, and theirs names a
// number known only as digits elsewhere.
func TestASlackUserIsOneOfThePeopleTheDirectoryKnows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, client := newFakeSlack(t)
	f.on("users.info", func(form map[string]string) any {
		var users []map[string]any
		for id := range strings.SplitSeq(form["users"], ",") {
			profile := map[string]string{"display_name": map[string]string{"U2": "dana", "U3": "sam"}[id]}
			if id == "U2" {
				profile["phone"] = "+1 555 010 0001"
			}
			if id == "U3" {
				profile["phone"] = "0501234567" // not international: no number to link
			}
			users = append(users, map[string]any{"id": id, "profile": profile})
		}
		return map[string]any{"ok": true, "users": users}
	})
	a, w := connectedTo(t, client)
	if err := a.cache.SetPeople(ctx, "whatsapp:1", []domain.PersonName{
		{ID: domain.PhoneID("15550100001"), Name: "Dana Levi", Rank: domain.RankSaved},
	}, nil); err != nil {
		t.Fatal(err)
	}

	a.learnPeople(ctx, w, []string{"U2", "U3"})
	dir, err := a.cache.Directory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{personID("T1", "U2"): "Dana Levi", personID("T1", "U3"): "sam"} {
		if got, _, _ := dir.Name(id); got != want {
			t.Errorf("%s is %q, want %q", id, got, want)
		}
	}
	if ids := dir.Identifiers(personID("T1", "U3")); len(ids) != 1 {
		t.Errorf("sam's local number linked them to %v", ids)
	}
}
