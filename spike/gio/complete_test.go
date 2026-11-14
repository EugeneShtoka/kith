package main

import (
	"context"
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestCompletingOffersEmojiAndPeopleOnlyAtAWordsStart(t *testing.T) {
	u := &ui{timeline: timeline{membersAsked: true, members: []domain.Member{
		{UserID: "@ann:example.org", DisplayName: "Ann Lee"},
		{UserID: "@bob:example.org", DisplayName: "Bob"},
	}}}
	at := func(text string) *completion {
		r := []rune(text)
		return u.completing(context.Background(), r, len(r))
	}
	c := at("great :thumbs")
	if c == nil || c.start != len([]rune("great ")) || !slices.ContainsFunc(c.offers, func(o candidate) bool { return o.put == "👍" }) {
		t.Fatalf(":thumbs offers %+v", c)
	}
	c = at("hi @an")
	if c == nil || len(c.offers) != 1 || c.offers[0].put != "@Ann Lee " || c.offers[0].mention.UserID != "@ann:example.org" {
		t.Fatalf("@an offers %+v", c)
	}
	for _, text := range []string{"a:th", "10:30", ":t", "mail@an", "plain words"} {
		if c := at(text); c != nil {
			t.Errorf("%q offers %+v, want nothing", text, c)
		}
	}
}
