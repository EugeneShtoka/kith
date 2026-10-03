package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// The rail rows the starter config's tags make, by key.
const (
	homeGroupKey     = "tag:All"
	dmsGroupKey      = "tag:DMs"
	unreadGroupKey   = "tag:Unread"
	draftsGroupKey   = "tag:Drafts"
	pinnedGroupKey   = "tag:Pinned"
	archivedGroupKey = "tag:Archived"
	spamGroupKey     = "tag:Spam"
	inviteGroupKey   = "tag:Invites"
)

// starter is the config a first run writes (default.toml), loaded as kith loads it.
func starter(t testing.TB) config.Config {
	t.Helper()
	cfg, err := starterOnce()
	if err != nil {
		t.Fatalf("the starter config does not load: %v", err)
	}
	return cfg
}

var starterOnce = sync.OnceValues(func() (config.Config, error) {
	dir, err := os.MkdirTemp("", "kith-starter-")
	if err != nil {
		return config.Config{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "config.toml")
	filled := strings.NewReplacer(
		`homeserver = ""`, `homeserver = "https://example.org"`,
		`user       = ""`, `user       = "@me:example.org"`,
	).Replace(config.Annotated())
	if err := os.WriteFile(path, []byte(filled), 0o600); err != nil {
		return config.Config{}, err
	}
	return config.Load(path)
})

// starterNew is New as a first run has it: the starter config's tags and rail order
// applied, display's other settings kept.
func starterNew(backend api.Backend, display config.Display) Model {
	cfg, err := starterOnce()
	if err != nil {
		panic(err)
	}
	if len(display.Rail.Order) == 0 {
		display.Rail.Order = cfg.Display.Rail.Order
	}
	cfg.Display = display
	return New(context.Background(), backend, display).WithConfigFile("", cfg)
}

// starterView is view with the starter config's tags.
func starterView(t testing.TB, view unreadView) unreadView {
	t.Helper()
	tags, _, err := setup.Tags(starter(t))
	if err != nil {
		t.Fatal(err)
	}
	view.tags = tags
	return view
}

// threeTags is a view with three tags: All (every room), DMs and Unread.
func threeTags(t testing.TB) unreadView {
	t.Helper()
	tags, _, err := setup.Tags(config.Config{Tags: []config.Tag{
		{Name: "All", Rule: []string{"*"}}, {Name: "DMs", Rule: []string{"dm"}}, {Name: "Unread", Rule: []string{"unread"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return unreadView{tags: tags}
}
