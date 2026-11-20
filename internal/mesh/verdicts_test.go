package mesh_test

import (
	"crypto/ecdsa"
	"path/filepath"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh"
)

// verdictStore is a store that is its own mesh's genesis, its bridge's key, and a
// write of verdicts signed by it.
func verdictStore(t *testing.T) (*mesh.Store, *ecdsa.PrivateKey, func(mv domain.MeshVerdicts)) {
	t.Helper()
	key, self := newKey(t)
	s := open(t, filepath.Join(t.TempDir(), "mesh.db"), storeKey(t))
	t.Cleanup(func() { _ = s.Close() })
	if err := s.ChangeRoster(t.Context(), key, []domain.Device{{ID: self, Role: domain.RoleBridge, AddedBy: self}}, nil); err != nil {
		t.Fatal(err)
	}
	return s, key, func(mv domain.MeshVerdicts) {
		t.Helper()
		if err := s.WriteVerdicts(t.Context(), key, mv); err != nil {
			t.Fatal(err)
		}
	}
}

// A verdict is written only over one it wins over, as the store reads it when it
// writes: a move never over a release, whenever made; a clearing deletes the release,
// and a move then writes again.
func TestAVerdictWriteNeverUndoesARelease(t *testing.T) {
	t.Parallel()
	s, _, write := verdictStore(t)
	at := time.Now().Truncate(time.Millisecond)
	tag := func(v domain.TagVerdict) domain.MeshVerdicts {
		return domain.MeshVerdicts{Tags: []domain.TagVerdict{v}}
	}
	write(tag(domain.TagVerdict{Room: "tg:-1001", Tag: "Spam", Released: true, At: at}))
	write(tag(domain.TagVerdict{Room: "tg:-1001", Tag: "spam", Rule: domain.MoveAlways, At: at.Add(time.Hour)}))
	if got := s.Verdicts().Tags; len(got) != 1 || !got[0].Released || got[0].Tag != "spam" {
		t.Fatalf("Verdicts = %+v, want the release", got)
	}
	write(tag(domain.TagVerdict{Room: "tg:-1001", Tag: "spam", At: at.Add(time.Minute)}))
	if got := s.Verdicts().Tags; len(got) != 0 {
		t.Fatalf("a clearing left %+v", got)
	}
	write(tag(domain.TagVerdict{Room: "tg:-1001", Tag: "spam", Rule: domain.MoveAlways, At: at.Add(time.Hour)}))
	if got := s.Verdicts().Tags; len(got) != 1 || !got[0].In() {
		t.Errorf("Verdicts = %+v, want the move", got)
	}
}

// A merged chat's verdicts move under the chat it joined, and its person's under the
// joined chat's person, each unless that one's own wins over it: a release under the
// merged chat over a move under the one it joined. Published again, nothing moves. One
// written under the merged person later (a bridge yet to learn of the merge) is left
// out, to move at the next publish.
func TestAMergedChatsVerdictsMoveUnderTheChatItJoined(t *testing.T) {
	t.Parallel()
	s, key, write := verdictStore(t)
	at := time.Now().Truncate(time.Millisecond)
	const lid, number = domain.RoomID("whatsapp:1/88000@lid"), domain.RoomID("whatsapp:1/15550@s.whatsapp.net")
	const lidPerson, numberPerson = "whatsapp:88000@lid", "whatsapp:15550@s.whatsapp.net"
	write(domain.MeshVerdicts{Tags: []domain.TagVerdict{
		{Room: lid, Tag: "spam", Released: true, At: at},
		{Room: lid, Tag: "work", Rule: domain.MoveAlways, At: at},
		{Room: number, Tag: "spam", Rule: domain.MoveAlways, At: at.Add(time.Minute)},
	}, People: []domain.PersonVerdict{
		{Person: lidPerson, Tag: "spam", Released: true, At: at},
		{Person: lidPerson, Tag: "work", Rule: domain.MoveSenderFirst, At: at},
		{Person: numberPerson, Tag: "spam", Rule: domain.MoveSenderFirst, At: at.Add(time.Minute)},
	}})
	if err := s.PublishAliases(t.Context(), key, map[domain.RoomID]domain.RoomID{lid: number}); err != nil {
		t.Fatal(err)
	}
	got := s.Verdicts().Tags
	if len(got) != 2 || got[0].Room != number || !got[0].Released || got[1].Room != number || !got[1].In() {
		t.Fatalf("Verdicts = %+v, want the number's: spam released, work moved", got)
	}
	people := s.Verdicts().People
	if len(people) != 2 || people[0].Person != numberPerson || !people[0].Released || people[1].Person != numberPerson || !people[1].In() {
		t.Fatalf("Verdicts().People = %+v, want the number's: spam released, work moved", people)
	}
	changed := s.Changed()
	if err := s.PublishAliases(t.Context(), key, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
		t.Error("published again, something moved")
	default:
	}
	write(domain.MeshVerdicts{People: []domain.PersonVerdict{{Person: lidPerson, Tag: "news", Rule: domain.MoveSenderFirst, At: at}}})
	if people := s.Verdicts().People; len(people) != 2 {
		t.Errorf("Verdicts().People = %+v, want the number's two alone", people)
	}
}
