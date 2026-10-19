//go:build signal

package meow

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
)

// A vote names its poll by the author's ACI and the poll's sent time, its answers by
// place, and its number among the voter's votes; an author that is no ACI is left out.
func TestAVoteNamesItsPollAndAnswers(t *testing.T) {
	t.Parallel()
	author := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	v := vote("chat", true, "sender", &signalpb.DataMessage_PollVote{
		TargetAuthorAciBinary: author[:], TargetSentTimestamp: new(uint64(7)), OptionIndexes: []uint32{0, 2}, VoteCount: new(uint32(3)),
	})
	if v.GetTargetAuthor() != author.String() || v.GetTargetSent() != 7 || !slices.Equal(v.GetOptions(), []uint32{0, 2}) || v.GetRound() != 3 || !v.GetGroup() {
		t.Errorf("vote %v", v)
	}
	if v := vote("chat", false, "sender", &signalpb.DataMessage_PollVote{TargetAuthorAciBinary: []byte{1, 2}}); v.GetTargetAuthor() != "" {
		t.Errorf("a short author became %q", v.GetTargetAuthor())
	}
}
