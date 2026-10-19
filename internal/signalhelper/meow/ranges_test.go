//go:build signal

package meow

import (
	"testing"

	"github.com/google/uuid"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/backuppb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
)

// An archived body keeps its mentions and styles as a message carries them, styles
// numbered alike; a range that is neither is dropped.
func TestAnArchivedBodyKeepsItsMentionsAndStyles(t *testing.T) {
	t.Parallel()
	who := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	got := archivedRanges([]*backuppb.BodyRange{
		{Start: 0, Length: 1, AssociatedValue: &backuppb.BodyRange_MentionAci{MentionAci: who[:]}},
		{Start: 2, Length: 4, AssociatedValue: &backuppb.BodyRange_Style_{Style: backuppb.BodyRange_SPOILER}},
		{Start: 7, Length: 1, AssociatedValue: &backuppb.BodyRange_Style_{Style: backuppb.BodyRange_NONE}},
	})
	if len(got) != 2 {
		t.Fatalf("ranges %v, want the mention and the spoiler", got)
	}
	if got[0].GetMentionAci() != who.String() || got[0].GetStart() != 0 {
		t.Errorf("mention %v", got[0])
	}
	if got[1].GetStyle() != signalpb.BodyRange_SPOILER || got[1].GetStart() != 2 || got[1].GetLength() != 4 {
		t.Errorf("style %v, want a spoiler over 2+4", got[1])
	}
	if s := styles(got); len(s) != 1 || s[0].GetKind().String() != "KIND_SPOILER" {
		t.Errorf("the wire's styles %v, want the spoiler", s)
	}
}
