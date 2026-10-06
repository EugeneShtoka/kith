package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// foldered is a backend whose Telegram account 42 has one folder, Friends: two of its
// chats.
type foldered struct{ fakeBackend }

func (*foldered) Groupings(_ context.Context, network domain.Protocol, account string) (domain.RoomOwner, []domain.Grouping, error) {
	if network != domain.ProtocolTelegram || account != "personal" {
		return "", nil, nil
	}
	return domain.AccountRooms(domain.ProtocolTelegram, "42"), []domain.Grouping{
		{Name: "Friends", Rooms: []domain.RoomID{"telegram:42/7", "telegram:42/8"}},
	}, nil
}

// The preview says what copying a folder would change, among the account's rooms only;
// the copy writes it into the config file, the tag made where none was, and a
// WhatsApp chat in the tag left alone.
func TestAFolderIsPreviewedAndCopiedIntoTheConfig(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("homeserver = \"https://x\"\nuser = \"@me:x\"\n\n[[tag]]\nname = \"Friends\"\npicked = [\"whatsapp:44/1@s.whatsapp.net\", \"telegram:42/9\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &foldered{fakeBackend{Nop: apitest.Nop{}, rooms: []domain.Room{
		{ID: "telegram:42/7", Name: "Dana"}, {ID: "telegram:42/8", Name: "Sam"}, {ID: "telegram:42/9", Name: "Old"},
		{ID: "whatsapp:44/1@s.whatsapp.net", Name: "Mom"},
	}}}
	h := serve(t, backend, func(d *daemon.Daemon) { d.Config = daemon.NewConfigFile(path, "") })
	r := h.client()
	ctx := t.Context()
	diffs, err := r.PreviewGroupings(ctx, "telegram", "personal")
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 1 || !diffs[0].Exists || len(diffs[0].Add) != 2 || !slices.Equal(diffs[0].Remove, []domain.RoomID{"telegram:42/9"}) {
		t.Fatalf("preview = %+v", diffs)
	}
	if aerr := r.ApplyGroupings(ctx, "telegram", "personal", map[string]domain.GroupingChoice{"Friends": domain.TakeNetworks}); aerr != nil {
		t.Fatal(aerr)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	picked := cfg.Tags[0].Picked
	slices.Sort(picked)
	if want := []string{"telegram:42/7", "telegram:42/8", "whatsapp:44/1@s.whatsapp.net"}; !slices.Equal(picked, want) {
		t.Errorf("Friends picks %v, want %v", picked, want)
	}
}
