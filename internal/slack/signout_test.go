package slack

import (
	"context"
	"errors"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Signing an account out signs its session out of Slack (a session Slack ended
// already counts), stops its connection and deletes its credentials, its rooms
// staying readable or going as asked; a connection made on its old sign-in is not
// adopted after; signed out, it is not connected, and another space is no account's.
func TestSigningOutEndsTheSlackSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		forget bool
		answer map[string]any
	}{
		{false, map[string]any{"ok": true}},
		{true, map[string]any{"ok": false, "error": "invalid_auth"}},
	} {
		f, client := newFakeSlack(t)
		f.on("auth.signout", func(map[string]string) any { return tc.answer })
		a, w := connectedTo(t, client)
		secrets, _ := a.secrets.(*memSecrets)
		signedIn(t, secrets, "work")
		room := roomID("T1", "C1")
		if err := a.cache.SaveRooms(ctx, domain.AccountRooms(domain.ProtocolSlack, "T1"), []domain.Room{{ID: room, Name: "general"}}); err != nil {
			t.Fatal(err)
		}

		if err := a.SignOut(ctx, workspaceSpaceID("T9"), tc.forget); !errors.Is(err, api.ErrNotOnNetwork) {
			t.Errorf("signing out a workspace no account is = %v, want refused", err)
		}
		if err := a.SignOut(ctx, workspaceSpaceID("T1"), tc.forget); err != nil {
			t.Fatal(err)
		}
		if n := len(f.calls("auth.signout")); n != 1 {
			t.Errorf("forget %v: Slack was asked to sign out %d times, want once", tc.forget, n)
		}
		if _, kept := secrets.values[credentialsRef("work")]; kept {
			t.Errorf("forget %v: the credentials are still kept", tc.forget)
		}
		if len(a.connected()) != 0 {
			t.Errorf("forget %v: still connected", tc.forget)
		}
		if a.adopt(newWorkspace(w.account, w.creds, "Acme", client, w.signIn)) {
			t.Errorf("forget %v: a connection on the old sign-in was adopted", tc.forget)
		}
		rooms, _ := a.Rooms(ctx)
		if kept := len(rooms) == 1; kept == tc.forget {
			t.Errorf("forget %v: rooms after = %v", tc.forget, rooms)
		}
		if err := a.SignOut(ctx, workspaceSpaceID("T1"), tc.forget); err == nil {
			t.Errorf("forget %v: signing out again succeeded", tc.forget)
		}
	}
}
