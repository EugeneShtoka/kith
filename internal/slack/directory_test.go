package slack

import (
	"context"
	"strings"
	"testing"
	"time"

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

// Every member of a workspace is in the directory once it connects, page after page:
// someone not yet seen in a message is named, linked to their number; someone deleted
// is not.
func TestEveryMemberOfAWorkspaceIsInTheDirectory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, client := newFakeSlack(t)
	f.on("users.list", func(form map[string]string) any {
		if form["cursor"] == "" {
			return map[string]any{"ok": true, "members": []any{
				map[string]any{"id": "U7", "profile": map[string]string{"display_name": "fay", "phone": "+1 555 010 0007"}},
				map[string]any{"id": "U8", "deleted": true, "profile": map[string]string{"display_name": "gone"}},
			}, "response_metadata": map[string]string{"next_cursor": "page2"}}
		}
		return map[string]any{"ok": true, "members": []any{
			map[string]any{"id": "U9", "profile": map[string]string{"real_name": "Gil Stone"}},
		}}
	})
	a, w := connectedTo(t, client)
	if err := a.cache.SetPeople(ctx, "telegram:1", []domain.PersonName{
		{ID: domain.PhoneID("15550100007"), Name: "Fay Saved", Rank: domain.RankSaved},
	}, nil); err != nil {
		t.Fatal(err)
	}

	a.listEveryone(ctx, w)
	dir, err := a.cache.Directory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		personID("T1", "U7"): "Fay Saved", personID("T1", "U9"): "Gil Stone", personID("T1", "U8"): "",
	} {
		if got, _, _ := dir.Name(id); got != want {
			t.Errorf("%s is %q, want %q", id, got, want)
		}
	}
	if pages := len(f.calls("users.list")); pages != 2 {
		t.Errorf("users.list asked %d times, want both pages", pages)
	}
}

// Connecting lists the workspace's members, with the catch-up, in the background.
func TestConnectingListsTheWorkspace(t *testing.T) {
	t.Parallel()
	f, client := newFakeSlack(t)
	f.on("users.list", func(map[string]string) any { return map[string]any{"ok": true, "members": []any{}} })
	a, w := connectedTo(t, client)
	a.goCatchUp(context.Background(), w)
	deadline := time.Now().Add(5 * time.Second)
	for len(f.calls("users.list")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("connecting never listed the workspace")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
