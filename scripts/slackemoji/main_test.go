package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
	"github.com/EugeneShtoka/kith/internal/session"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// kept is a message as the cache holds it, before and after the conversion.
type kept struct {
	room                     domain.RoomID
	id                       string
	body, html               string
	wantBody, wantHTML, bold string // bold: the words the bold span covers after
}

var (
	slackRoom  = domain.RoomID(domain.NativeID(domain.ProtocolSlack, "T1", "C1"))
	matrixRoom = domain.RoomID(domain.NativeID(domain.ProtocolMatrix, "acct", "!r:example.org"))
)

// What the cache held before the adapter converted: each kind of Slack message turns
// out as the adapter now keeps it, and nothing of another network's changes.
var cases = []kept{
	{room: slackRoom, id: "1.0", body: "Idk why :neutral_face:", wantBody: "Idk why 😐"},
	{room: slackRoom, id: "2.0", body: "at 10:30:00 a:b:c", wantBody: "at 10:30:00 a:b:c"},
	{
		room: slackRoom, id: "3.0", body: "big news :tada: today", html: "<b>big news</b> :tada: <i>today</i>",
		wantBody: "big news 🎉 today", wantHTML: "<b>big news</b> 🎉 <i>today</i>", bold: "big news",
	},
	{
		room: slackRoom, id: "4.0", body: "run :smile: :wave::skin-tone-3:", html: "<code>run :smile:</code> :wave::skin-tone-3:",
		wantBody: "run :smile: 👋\U0001F3FC", wantHTML: "<code>run :smile:</code> 👋\U0001F3FC",
	},
	{
		room: slackRoom, id: "5.0", body: ":tada: notes", html: `<a href="https://x.io/:tada:">:tada: notes</a>`,
		wantBody: ":tada: notes", wantHTML: `<a href="https://x.io/:tada:">:tada: notes</a>`,
	},
	{room: matrixRoom, id: "6.0", body: "typed :tada: here", wantBody: "typed :tada: here"},
}

// The conversion refuses while kithd holds its lock, lists without writing on a dry run,
// converts every Slack message, its formatting and its kept versions with the search
// index once, keeps a copy first, tells clients to reload, and changes nothing the
// second time.
func TestTheCacheConvertsOnce(t *testing.T) {
	keyring.MockInit()
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Config{Storage: config.Storage{
		Instance: "t", DataDir: filepath.Join(dir, "data"), StateDir: filepath.Join(dir, "state"),
		CacheDir: filepath.Join(dir, "cache"), RuntimeDir: filepath.Join(dir, "run"),
	}}
	path := filepath.Join(dir, "config.toml")
	if serr := config.Save(path, cfg); serr != nil {
		t.Fatal(serr)
	}
	storage, err := setup.StorageFor(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	key, err := session.LoadOrCreateStoreKey(storage.KeyringService, storage.Instance, "cache")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(storage.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	edited := domain.EventID(string(slackRoom) + "/1.0")
	cache := open(t, storage, key)
	for _, c := range cases {
		msg := domain.Message{
			ID: domain.EventID(string(c.room) + "/" + c.id), RoomID: c.room, Sender: "someone",
			Body: c.body, Format: richtext.FromMarkup(c.html), Timestamp: time.Unix(1000, 0),
		}
		if err = cache.SaveMessages(ctx, c.room, []domain.Message{msg}); err != nil {
			t.Fatal(err)
		}
	}
	if err = cache.SaveRevisionsFor(ctx, slackRoom, edited, []domain.Revision{
		{ID: edited, Body: "Idk :smile:", At: time.Unix(1000, 0)},
		{ID: edited + "e", Body: "Idk why :neutral_face:", At: time.Unix(1001, 0)},
	}); err != nil {
		t.Fatal(err)
	}
	if hits := search(t, cache, "tada"); hits != 3 {
		t.Fatalf("before: %d hits for tada, want 3", hits)
	}
	epoch, _, err := cache.LastChange(ctx)
	if err != nil {
		t.Fatal(err)
	}
	closeCache(t, cache)

	lock, held, err := daemon.Acquire(storage)
	if err != nil || !held {
		t.Fatalf("take the lock: held %v, %v", held, err)
	}
	if err = run(ctx, path, false, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "kithd is running") {
		t.Errorf("with kithd running: %v, want a refusal", err)
	}
	if err = lock.Release(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err = run(ctx, path, true, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "would change 5 of 7 Slack messages and versions") {
		t.Errorf("dry run said:\n%s", out.String())
	}
	cache = open(t, storage, key)
	if got := body(t, cache, cases[0]); got != cases[0].body {
		t.Errorf("a dry run wrote %q", got)
	}
	closeCache(t, cache)

	out.Reset()
	if err = run(ctx, path, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "changed 5 of 7") {
		t.Errorf("run said:\n%s", out.String())
	}
	copies, _ := filepath.Glob(storage.CachePath() + ".before-slackemoji-*")
	if len(copies) != 1 {
		t.Errorf("copies kept: %v, want one", copies)
	}

	cache = open(t, storage, key)
	for _, c := range cases {
		msg, ok, merr := cache.MessageByID(ctx, c.room, domain.EventID(string(c.room)+"/"+c.id))
		if merr != nil || !ok {
			t.Fatalf("%s: %v %v", c.id, ok, merr)
		}
		if msg.Body != c.wantBody || msg.Format.Markup() != c.wantHTML {
			t.Errorf("%s: %q %q, want %q %q", c.id, msg.Body, msg.Format.Markup(), c.wantBody, c.wantHTML)
		}
		var bold string
		for _, s := range msg.Format.Spans() {
			if s.Bold {
				bold = msg.Body[s.Start:s.End]
			}
		}
		if bold != c.bold {
			t.Errorf("%s: bold over %q, want %q", c.id, bold, c.bold)
		}
	}
	revs, err := cache.Revisions(ctx, slackRoom, edited)
	if err != nil || len(revs) != 2 || revs[0].Body != "Idk 😄" || revs[1].Body != "Idk why 😐" {
		t.Errorf("versions: %+v (%v), want both converted", revs, err)
	}
	// The index follows: only the link's and the other network's words still say tada.
	if hits := search(t, cache, "tada"); hits != 2 {
		t.Errorf("after: %d hits for tada, want 2", hits)
	}
	if now, _, lerr := cache.LastChange(ctx); lerr != nil || now == epoch {
		t.Errorf("the change numbering kept its epoch %q (%v): clients would keep what they hold", now, lerr)
	}
	closeCache(t, cache)

	out.Reset()
	if err = run(ctx, path, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing to change in 7") {
		t.Errorf("second run said:\n%s", out.String())
	}
}

func open(t *testing.T, storage domain.Storage, key []byte) *db.Cache {
	t.Helper()
	cache, err := db.Open(context.Background(), storage.CachePath(), key)
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

func closeCache(t *testing.T, cache *db.Cache) {
	t.Helper()
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
}

func body(t *testing.T, cache *db.Cache, c kept) string {
	t.Helper()
	msg, _, err := cache.MessageByID(context.Background(), c.room, domain.EventID(string(c.room)+"/"+c.id))
	if err != nil {
		t.Fatal(err)
	}
	return msg.Body
}

func search(t *testing.T, cache *db.Cache, q string) int {
	t.Helper()
	hits, err := cache.SearchMessages(context.Background(),
		domain.SearchRequest{Filter: domain.ParseSearch(q, time.Now()), Rooms: domain.EveryRoom(), Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	return len(hits)
}
