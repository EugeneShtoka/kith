package chatexport

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/EugeneShtoka/kith/internal/domain"
)

const (
	// sampleMost bounds the messages looked for in the cache; sampleLeast is the
	// shortest that says one message alone ("ok" is in every chat).
	sampleMost  = 300
	sampleLeast = 16
	// likelyMost bounds the members offered for a name.
	likelyMost = 3
)

// Sample is up to sampleMost of the export's distinct longer messages, newest first:
// the likeliest to be cached already.
func (e Export) Sample() []string {
	seen := map[string]bool{}
	var out []string
	for i := len(e.Lines) - 1; i >= 0 && len(out) < sampleMost; i-- {
		b := e.Lines[i].Body
		if utf8.RuneCountInString(b) >= sampleLeast && !seen[b] {
			seen[b] = true
			out = append(out, b)
		}
	}
	return out
}

// Rooms are the rooms the export may be, of rooms: those that cached its messages
// (found, by room) the most first, and those its file is named after.
func (e Export) Rooms(rooms []domain.Room, found map[domain.RoomID]int) []domain.ExportRoom {
	title := fold(e.Title)
	var out []domain.ExportRoom
	for i := range rooms {
		r := &rooms[i]
		names := []string{r.Name}
		if r.Name == "" && len(r.Members) == 1 {
			names = r.Members // a chat with one person is named after them
		}
		named := slices.ContainsFunc(names, func(n string) bool {
			n = fold(n)
			return n != "" && (title == n || strings.HasSuffix(title, " "+n))
		})
		if found[r.ID] > 0 || named {
			out = append(out, domain.ExportRoom{ID: r.ID, Found: found[r.ID], Named: named})
		}
	}
	slices.SortStableFunc(out, func(a, b domain.ExportRoom) int {
		return cmp.Or(cmp.Compare(b.Found, a.Found), boolCompare(b.Named, a.Named))
	})
	return out
}

// Adds counts the messages an import adds to a room whose oldest cached message is at
// before.
func (e Export) Adds(before time.Time) int {
	n := 0
	for _, l := range e.Lines {
		if l.older(before) {
			n++
		}
	}
	return n
}

// older reports whether l was sent before before (zero: always): the whole of its
// minute when the phone wrote no seconds, so a message cached in that minute is never
// added again.
func (l Line) older(before time.Time) bool {
	span := time.Second
	if l.Minute {
		span = time.Minute
	}
	return before.IsZero() || !l.At.Add(span).After(before)
}

// Senders matches the export's names to members, the name with the most messages
// first. cached is the room's messages saying what Sample does: a name whose messages
// the room cached in the same minute is who sent them most often; else a name is the
// one member called so, if one is.
func (e Export) Senders(members []domain.Member, cached []domain.Message) []domain.ExportSender {
	byBody := map[string][]int{} // indexes into cached
	known := map[string]domain.Member{}
	for i := range cached {
		m := &cached[i]
		byBody[m.Body] = append(byBody[m.Body], i)
		known[m.Sender] = domain.Member{UserID: m.Sender, DisplayName: m.SenderName}
	}
	for _, m := range members {
		known[m.UserID] = m
	}
	counts := map[string]int{}
	votes := map[string]map[string]int{}
	var names []string
	for _, l := range e.Lines {
		if counts[l.Sender] == 0 {
			names = append(names, l.Sender)
			votes[l.Sender] = map[string]int{}
		}
		counts[l.Sender]++
		for _, i := range byBody[l.Body] {
			if d := cached[i].Timestamp.Sub(l.At); cached[i].Sender != "" && d > -time.Minute && d < time.Minute {
				votes[l.Sender][cached[i].Sender]++
			}
		}
	}
	slices.SortStableFunc(names, func(a, b string) int { return cmp.Compare(counts[b], counts[a]) })
	out := make([]domain.ExportSender, 0, len(names))
	for _, name := range names {
		s := domain.ExportSender{Name: name, Messages: counts[name]}
		voted := sortedBy(votes[name])
		var same []domain.Member
		for _, m := range members {
			if fold(m.Name()) == fold(name) {
				same = append(same, m)
			}
		}
		switch {
		case len(voted) > 0:
			s.Chosen = voted[0]
		case len(same) == 1:
			s.Chosen = same[0].UserID
		}
		s.Likely = likely(name, voted, known, members)
		out = append(out, s)
	}
	return out
}

// likely is up to likelyMost members name may be: those voted for, the most votes
// first, then the members whose names share the most words with it.
func likely(name string, voted []string, known map[string]domain.Member, members []domain.Member) []domain.Member {
	var out []domain.Member
	for _, id := range voted {
		out = appendNew(out, known[id])
	}
	alike := slices.Clone(members)
	slices.SortStableFunc(alike, func(a, b domain.Member) int { return cmp.Compare(likeness(name, b), likeness(name, a)) })
	for _, m := range alike {
		if len(out) < likelyMost && likeness(name, m) > 0 {
			out = appendNew(out, m)
		}
	}
	return out[:min(len(out), likelyMost)]
}

// Messages are the messages an import adds to room: those older than before (see
// older), from senders' members (name → user ID; "" keeps the name alone), named as
// members call them. Each ID is made of what its message says, so importing the same
// export again adds nothing twice.
func (e Export) Messages(room domain.RoomID, before time.Time, senders map[string]string, members []domain.Member) []domain.Message {
	names := map[string]string{}
	for _, m := range members {
		names[m.UserID] = m.DisplayName
	}
	seen := map[string]int{}
	var out []domain.Message
	for i, l := range e.Lines {
		if !l.older(before) {
			continue
		}
		key := fmt.Sprintf("%s\x00%d\x00%s\x00%s", room, l.At.Unix(), l.Sender, l.Body)
		seen[key]++ // the same words twice in a minute are two messages
		sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d", key, seen[key]))
		sender, name := senders[l.Sender], l.Sender
		if names[sender] != "" {
			name = names[sender]
		}
		out = append(out, domain.Message{
			ID: domain.EventID("import:" + hex.EncodeToString(sum[:16])), RoomID: room,
			Sender: sender, SenderName: name, Body: l.Body, Timestamp: l.At, Seq: int64(i + 1),
		})
	}
	return out
}

// fold is a name compared: lower case, spaces collapsed.
func fold(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// likeness counts the words name and m's name share.
func likeness(name string, m domain.Member) int {
	theirs := strings.Fields(fold(m.Name()))
	n := 0
	for w := range strings.FieldsSeq(fold(name)) {
		if slices.Contains(theirs, w) {
			n++
		}
	}
	return n
}

// sortedBy is votes' keys, the most votes first.
func sortedBy(votes map[string]int) []string {
	keys := make([]string, 0, len(votes))
	for k := range votes {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int { return cmp.Or(cmp.Compare(votes[b], votes[a]), strings.Compare(a, b)) })
	return keys
}

// appendNew appends m unless one with its ID is there.
func appendNew(ms []domain.Member, m domain.Member) []domain.Member {
	if slices.ContainsFunc(ms, func(x domain.Member) bool { return x.UserID == m.UserID }) {
		return ms
	}
	return append(ms, m)
}

func boolCompare(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}
