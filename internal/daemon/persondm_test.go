package daemon_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// dmCase is one combination: the room, what you did in it, the tag, and how the person
// in it is held.
type dmCase struct {
	direct, mine, released, excluded, muted bool
	// held: "" (not held), "listed", "linked" (listed by their other ID), "marked".
	held string
}

func (c dmCase) String() string {
	return fmt.Sprintf("direct %v mine %v released %v excluded %v muted %v held %q", c.direct, c.mine, c.released, c.excluded, c.muted, c.held)
}

// moves is the rule as written: a direct message you never wrote in, not released or
// excluded, with someone a muted tag holds by any of their IDs.
func (c dmCase) moves() bool {
	return c.direct && !c.mine && !c.released && !c.excluded && c.muted && c.held != ""
}

// Every combination: a direct message moves into the muted tag holding its person, by
// the direct move's rails, whether the person is listed, listed by a linked ID, or
// marked by a filter; and both triggers move it (someone becoming hidden, and their
// message arriving).
func TestADirectMessageMovesWithItsPerson(t *testing.T) {
	t.Parallel()

	var cases []dmCase
	for i := range 1 << 5 {
		for _, held := range []string{"", "listed", "linked", "marked"} {
			cases = append(cases, dmCase{
				direct: i&1 != 0, mine: i&2 != 0, released: i&4 != 0, excluded: i&8 != 0, muted: i&16 != 0, held: held,
			})
		}
	}
	for _, c := range cases {
		for _, trigger := range []string{"hidden", "message"} {
			room, name := domain.RoomID(chatRm), chatNam
			if c.direct {
				room, name = dmRoom, dmName
			}
			src := &personBackend{spamBackend: spamWorld()}
			at := time.Now().Add(-time.Hour)
			src.timeline[room] = []domain.Message{{ID: mx + "$p1", RoomID: room, Sender: puppet, Body: "hi", Timestamp: at}}
			if c.mine {
				src.timeline[room] = append(src.timeline[room], domain.Message{ID: mx + "$m1", RoomID: room, Sender: me, Body: "who?", Timestamp: at.Add(time.Minute)})
			}
			if c.released {
				src.store(domain.TagVerdict{Room: room, Tag: "spam", Released: true})
			}
			cfg := spamConfig(withFilters(), spamTag(func(tag *config.Tag) {
				if !c.muted {
					tag.Notifies = nil
				}
				if c.excluded {
					tag.Excluded = []string{"room:" + name}
				}
				switch c.held {
				case "listed":
					tag.People = []string{puppet}
				case "linked":
					tag.People = []string{native}
				}
			}))
			if c.held == "marked" {
				src.people = []domain.PersonVerdict{{Person: puppet, Tag: "spam", Rule: domain.MoveSenderSpread, Filter: "runs"}}
			}
			n := personNotifier(t, cfg, src)
			dir := domain.NewDirectory(nil, []domain.PersonLink{{Source: "book", ID: puppet, Other: native}})
			n.UseDirectory(func(context.Context) (domain.Directory, error) { return dir, nil })
			ctx := context.Background()
			switch trigger {
			case "hidden":
				n.MoveWithHidden(ctx, []domain.RoomID{room})
			case "message":
				n.UseHidden(func(context.Context, domain.Message) (bool, error) { return c.held != "" && c.muted, nil })
				n.Deliver(ctx, domain.Message{ID: mx + "$p2", RoomID: room, Sender: puppet, Body: "hello", Timestamp: time.Now()})
			}
			got := src.verdicts()
			moved := len(got) == 1 && got[0].Room == room && got[0].Rule == domain.MoveWithPerson
			// A message from someone not hidden is not this rule's to judge.
			want := c.moves() && (trigger == "hidden" || c.held != "")
			if moved != want || len(got) > 1 {
				t.Fatalf("%v by %s: moved %+v, want moved %v", c, trigger, got, want)
			}
			if moved && c.held == "marked" && got[0].Filter != "runs" {
				t.Errorf("%v: the move names filter %q, want the mark's", c, got[0].Filter)
			}
		}
	}
}
