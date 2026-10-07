package db

import (
	"context"
	"maps"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The book takes, for each number, the name it trusts most from any account or
// bridge: a saved name before a bridge's, a bridge's before one the person chose; a
// name that is a number names nobody; an account read again replaces what it knew.
func TestTheBookTakesTheMostTrustedNameFromAnyAccount(t *testing.T) {
	t.Parallel()
	c, ctx := openTemp(t), context.Background()
	if err := c.SetNumberNames(ctx, "whatsapp:111", []domain.NumberName{
		{Phone: "15550100001", Name: "Dana Saved", Rank: domain.RankSaved},
		{Phone: "15550100002", Name: "Eli's push name", Rank: domain.RankChosen},
		{Phone: "15550100003", Name: "+15550100003", Rank: domain.RankSaved},
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetNumberNames(ctx, "telegram:42", []domain.NumberName{
		{Phone: "15550100001", Name: "Dana on Telegram", Rank: domain.RankChosen},
		{Phone: "15550100004", Name: "Fay", Rank: domain.RankSaved},
	}); err != nil {
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
	book, err := c.PhoneBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.PhoneBook{
		"15550100001": "Dana Saved",  // saved on WhatsApp, before Telegram's chosen
		"15550100002": "Eli Bridged", // the il bridge's, before the push name; bg's number is no name
		"15550100003": "Gil",         // a saved "name" that is the number is passed over
		"15550100004": "Fay",
	}
	if !maps.Equal(book, want) {
		t.Errorf("book = %v, want %v", book, want)
	}

	if err := c.SetNumberNames(ctx, "telegram:42", nil); err != nil {
		t.Fatal(err)
	}
	if book, _ = c.PhoneBook(ctx); book["15550100004"] != "" || book["15550100001"] != "Dana Saved" {
		t.Errorf("after Telegram knows nobody: %v", book)
	}
}
