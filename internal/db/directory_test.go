package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// tel names a number's identifier.
func tel(digits string) string { return domain.PhoneID(digits) }

// The directory takes, for each number, the name it trusts most from any account or
// bridge: a saved name before a bridge's, a bridge's before one the person chose; a
// name that is a number names nobody; an account read again replaces what it knew.
func TestTheDirectoryTakesTheMostTrustedNameFromAnyAccount(t *testing.T) {
	t.Parallel()
	c, ctx := openTemp(t), context.Background()
	if err := c.SetPeople(ctx, "whatsapp:111", []domain.PersonName{
		{ID: tel("15550100001"), Name: "Dana Saved", Rank: domain.RankSaved},
		{ID: tel("15550100002"), Name: "Eli's push name", Rank: domain.RankChosen},
		{ID: tel("15550100003"), Name: "+15550100003", Rank: domain.RankSaved},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPeople(ctx, "telegram:42", []domain.PersonName{
		{ID: tel("15550100001"), Name: "Dana on Telegram", Rank: domain.RankChosen},
		{ID: tel("15550100004"), Name: "Fay", Rank: domain.RankSaved},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.SaveMembers(ctx, "!dm:x", []domain.Member{
		{UserID: "@whatsapp_bg_15550100002:x", DisplayName: "+15550100002 (WA)"},
		{UserID: "@whatsapp_il_15550100002:x", DisplayName: "Eli Bridged (WA)"},
		{UserID: "@whatsapp_il_15550100003:x", DisplayName: "Gil (WA)"},
		{UserID: "@telegram_15550100005:x", DisplayName: "Not a number's name"},
	}); err != nil {
		t.Fatal(err)
	}
	dir, err := c.Directory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for number, want := range map[string]string{
		"15550100001": "Dana Saved",  // saved on WhatsApp, before Telegram's chosen
		"15550100002": "Eli Bridged", // the il bridge's, before the push name; bg's number is no name
		"15550100003": "Gil",         // a saved "name" that is the number is passed over
		"15550100004": "Fay",
		"15550100005": "", // a puppet of a network with no numbers in its IDs
	} {
		if got, _ := dir.Named("+" + number); got != want {
			t.Errorf("+%s is named %q, want %q", number, got, want)
		}
	}

	if err := c.SetPeople(ctx, "telegram:42", nil, nil); err != nil {
		t.Fatal(err)
	}
	dir, _ = c.Directory(ctx)
	if fay, _ := dir.Named("+15550100004"); fay != "" {
		t.Errorf("after Telegram knows nobody, Fay is still named %q", fay)
	}
	if dana, _ := dir.Named("+15550100001"); dana != "Dana Saved" {
		t.Errorf("after Telegram knows nobody, Dana is %q", dana)
	}
}

// A cache from before the directory (v9, names kept by number in phone_names) opens
// with every name it had, as a number's: what an account knew is not lost.
func TestACacheFromBeforeTheDirectoryKeepsItsNames(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	c, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if cerr := c.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"DROP TABLE person_names", "DROP TABLE person_links",
		`CREATE TABLE phone_names (
	source TEXT    NOT NULL,
	phone  TEXT    NOT NULL,
	name   TEXT    NOT NULL,
	rank   INTEGER NOT NULL,
	PRIMARY KEY (source, phone)
) STRICT, WITHOUT ROWID`,
		"INSERT INTO phone_names VALUES ('whatsapp:111', '15550100001', 'Dana', 0), ('telegram:42', '15550100002', 'Eli', 2)",
		"PRAGMA user_version = 9",
	} {
		if _, xerr := raw.ExecContext(ctx, stmt); xerr != nil {
			t.Fatalf("%s: %v", stmt, xerr)
		}
	}
	if rerr := raw.Close(); rerr != nil {
		t.Fatal(rerr)
	}

	c, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	dir, err := c.Directory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for number, want := range map[string]string{"15550100001": "Dana", "15550100002": "Eli"} {
		if got, _ := dir.Named("+" + number); got != want {
			t.Errorf("after the upgrade +%s is %q, want %q", number, got, want)
		}
	}
	if _, rank, _ := dir.Name(domain.PhoneID("15550100002")); rank != domain.RankChosen {
		t.Errorf("Eli's rank = %v, want the chosen rank kept", rank)
	}
}

// A source read again replaces all it said — names and links — and leaves every other
// source's; of two names it gives one identifier the better ranked stays; a link to
// oneself or to nothing is not kept; and forgetting a prefix's sources spares those
// still read.
func TestASourceReplacesWhatItSaidOfPeople(t *testing.T) {
	t.Parallel()
	c, ctx := openTemp(t), context.Background()
	set := func(source string, names []domain.PersonName, links []domain.PersonLink) {
		t.Helper()
		if err := c.SetPeople(ctx, source, names, links); err != nil {
			t.Fatal(err)
		}
	}
	name := func(id string) string {
		t.Helper()
		dir, err := c.Directory(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got, _, _ := dir.Name(id)
		return got
	}
	set("bridge:a#1", []domain.PersonName{
		{ID: "telegram:7", Name: "Dana chosen", Rank: domain.RankChosen},
		{ID: "telegram:7", Name: "Dana saved", Rank: domain.RankSaved},
	}, []domain.PersonLink{{ID: "telegram:7", Other: tel("15550100001")}, {ID: "telegram:7", Other: "telegram:7"}, {ID: "", Other: "x"}})
	set("bridge:b#1", []domain.PersonName{{ID: tel("15550100002"), Name: "Eli", Rank: domain.RankSaved}}, nil)
	if got := name(tel("15550100001")); got != "Dana saved" {
		t.Fatalf("Dana's number = %q, want the saved name through the link", got)
	}
	var selfLinks int
	if err := c.db.QueryRowContext(ctx, "SELECT count(*) FROM person_links WHERE id = other OR id = ''").Scan(&selfLinks); err != nil || selfLinks != 0 {
		t.Errorf("%d links to oneself or to nothing kept (%v)", selfLinks, err)
	}

	set("bridge:a#1", []domain.PersonName{{ID: "telegram:7", Name: "Dana again", Rank: domain.RankChosen}}, nil)
	if got := name(tel("15550100001")); got != "" {
		t.Errorf("after a#1 dropped the link, Dana's number is still %q", got)
	}
	if got := name(tel("15550100002")); got != "Eli" {
		t.Errorf("another source's name went: %q", got)
	}

	if err := c.ForgetPeople(ctx, "bridge:", []string{"bridge:b#1"}); err != nil {
		t.Fatal(err)
	}
	if got, eli := name("telegram:7"), name(tel("15550100002")); got != "" || eli != "Eli" {
		t.Errorf("after forgetting all but b#1: Dana %q (want none), Eli %q (want kept)", got, eli)
	}
	if sources, err := c.PeopleSources(ctx, "bridge:"); err != nil || len(sources) != 1 || sources[0] != "bridge:b#1" {
		t.Errorf("sources = %v, %v", sources, err)
	}
}
