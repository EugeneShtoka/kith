package tui

import (
	"cmp"
	"image/color"
	"maps"
	"slices"
	"strconv"
	"sync/atomic"

	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// derivedCache holds what the timeline reads from every loaded message (thread
// structure, sender colors, name-column width) plus the rendered-row cache, per room,
// recomputed only when the messages change. It is behind a pointer so every Model
// copy shares it. Update fills it (primeTimeline) and View only reads it
// (TestViewLeavesTheSharedCacheAlone). View must still not run while Update writes:
// renderloop_test.go asserts the pinned Bubble Tea fork renders between Updates, on
// their goroutine, and cmdreach_test.go that no tea.Cmd reaches the cache.

// derivedKey identifies the message set a cached answer was computed from; rev is
// bumped by setMessages.
type derivedKey struct {
	room   domain.RoomID
	thread domain.EventID
	rev    uint64
	// cfg covers theme and identity settings, which a config reload can change.
	cfg uint64
	// unread fingerprints the room's unread state, which thread summary rows draw
	// and which changes without any message changing. A fingerprint, not a counter:
	// the unread state has no revision of its own.
	unread uint64
	// place fingerprints the room's name and spaces (placeFingerprint).
	place uint64
	// rtl: the timeline is mirrored (direction.go).
	rtl bool
	// selves fingerprints who this person is (isMe), which grows as accounts log in
	// and decides which rows are drawn as one's own.
	selves uint64
}

// fnvOffset and fnvMix are FNV-1a over strings, each followed by a separator so "1","23"
// cannot hash as "12","3".
const fnvOffset, fnvPrime = uint64(14695981039346656037), uint64(1099511628211)

func fnvMix(h uint64, s string) uint64 {
	for _, b := range []byte(s) {
		h = (h ^ uint64(b)) * fnvPrime
	}
	return (h ^ 0xff) * fnvPrime
}

// selvesFingerprint is an FNV-1a hash of the IDs that are this person, in order.
func selvesFingerprint(ids []string) uint64 {
	h := fnvOffset
	for _, id := range ids {
		h = fnvMix(h, id)
	}
	return h
}

// unreadFingerprint is an FNV-1a hash of a room's unread state, per-thread counts
// included.
func unreadFingerprint(u domain.Unread) uint64 {
	h := fnvMix(fnvOffset, strconv.Itoa(u.Notifications))
	h = fnvMix(h, strconv.Itoa(u.Highlights))
	for _, t := range u.Threads {
		h = fnvMix(h, string(t.Root))
		h = fnvMix(h, strconv.Itoa(t.Unread))
		h = fnvMix(h, strconv.Itoa(t.Mentions))
	}
	return h
}

// placeFingerprint is an FNV-1a hash of the open room as its messages' rows read it:
// its label, its spaces (the first sets the sender-name rule), its network, whether it
// is a DM, and the tags holding it (tracked words can be scoped by any of them). Space membership and room
// names change without any message changing.
func placeFingerprint(f domain.RoomFacts) uint64 {
	h := fnvMix(fnvOffset, f.ID)
	h = fnvMix(h, f.Name)
	for _, space := range f.Spaces {
		h = fnvMix(h, space)
	}
	h = fnvMix(h, f.Protocol.String())
	h = fnvMix(h, strconv.FormatBool(f.Direct))
	// The tags holding it: a home that picks name rules (homesOf); and the one its
	// network's archive puts it in, which decides some of them.
	for _, tag := range f.Tags {
		h = fnvMix(h, tag)
	}
	h = fnvMix(h, f.ArchivedIn)
	return h
}

// keyFor is the identity of everything derivedCache's answers depend on.
func (m Model) keyFor() derivedKey {
	k := derivedKey{
		room:   m.openRoom,
		thread: m.thread.root,
		rev:    m.timeline.rev,
		cfg:    m.conf.rev,
		unread: unreadFingerprint(m.unread[m.openRoom]),
		rtl:    m.mirrored(),
		selves: selvesFingerprint(m.selves),
	}
	if room, ok := m.roomByID(m.openRoom); ok {
		k.place = placeFingerprint(m.factsFor(room))
	}
	return k
}

// derivedCache holds the last answer and the key it was computed for.
type derivedCache struct {
	key     derivedKey
	valid   bool
	main    []domain.Message
	threads []domain.Thread
	// byAnchor is threads by the message they hang under ("" for an unloaded root).
	byAnchor map[domain.EventID][]domain.Thread
	// shown is what the timeline draws: the collapsed main timeline or one thread.
	shown []domain.Message
	// hasMedia is whether anything in shown carries a file.
	hasMedia bool
	// index is each shown message's position, and loaded each loaded message's in
	// m.timeline.messages (a thread-folded reply is loaded but not shown).
	index  map[domain.EventID]int
	loaded map[domain.EventID]int
	colors map[string]color.Color
	nameW  int
	// slots pins each identity group to the hue index it was first given in this
	// room. Slots are never renumbered, so a newly loaded sender cannot repaint
	// everyone else mid-scroll.
	slots     map[string]int
	slotsRoom domain.RoomID
	next      int
	// unsaved are slots assigned when colors were last computed, flushed to the store
	// by the next Update.
	unsaved map[string]int

	// rowCache is one entry per shown message, emptied when the message set, width
	// or name column changes.
	rowCache []rowEntry
	rowWidth int
	rowNameW int
	// suffix[i] is how many rows messages [i:] occupy, valid for i >= suffixFrom,
	// so finding the window costs the same at any scroll depth.
	suffix     []int
	suffixFrom int
	// inputsRev counts changes to what rows depend on besides the messages (reactions,
	// pictures); anchoredRev is the count anchorBelow last looked at.
	inputsRev, anchoredRev uint64
	// drops counts dropSuffixAt calls, so a layout can tell that a row it drew changed
	// height under the totals it was placed by.
	drops int
}

// resetRows drops every rendered row; narrower invalidation is per entry.
func (d *derivedCache) resetRows(n, width, nameW int) {
	d.rowCache = make([]rowEntry, n)
	d.suffix = make([]int, n+1)
	d.suffixFrom = n
	d.rowWidth, d.rowNameW = width, nameW
}

// prepareRows makes the row cache usable for a layout at this width.
func (d *derivedCache) prepareRows(width, nameW int) {
	if len(d.rowCache) != len(d.shown) || d.rowWidth != width || d.rowNameW != nameW {
		d.resetRows(len(d.shown), width, nameW)
	}
}

// dropSuffixAt marks the running row totals unusable at and below message i.
func (d *derivedCache) dropSuffixAt(i int) {
	d.drops++
	if d.suffixFrom <= i {
		d.suffixFrom = i + 1
	}
}

// extendTo walks further back until the running totals cover budget rows, or the
// oldest loaded message is reached.
func (m Model) extendTo(w walk, budget int) {
	m.extendWhile(w, func(d *derivedCache) bool { return d.suffix[d.suffixFrom] < budget })
}

// extendToIndex walks back far enough that message i's running total is known.
func (m Model) extendToIndex(w walk, i int) {
	m.extendWhile(w, func(d *derivedCache) bool { return d.suffixFrom > i })
}

// extendWhile extends the running totals one message at a time while more holds.
func (m Model) extendWhile(w walk, more func(*derivedCache) bool) {
	d := w.derived
	for d.suffixFrom > 0 && more(d) {
		i := d.suffixFrom - 1
		h := m.heightOf(w, i)
		// heightOf may have retracted suffixFrom on a height change; re-check.
		if d.suffixFrom != i+1 {
			continue
		}
		d.suffix[i] = d.suffix[i+1] + h
		d.suffixFrom = i
	}
}

// startFor is the oldest message the layout must draw to fill budget rows: the
// largest index whose running total still covers it (binary search).
func (d *derivedCache) startFor(budget int) int {
	lo, hi := d.suffixFrom, len(d.shown)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if mid > len(d.shown) || d.suffix[mid] < budget {
			hi = mid - 1
		} else {
			lo = mid
		}
	}
	return lo
}

// newDerivedCache is the empty cache a Model starts with.
func newDerivedCache() *derivedCache { return &derivedCache{slots: map[string]int{}} }

// frameClock says which Model the shared cache was last made final for. Shared by
// every copy (a pointer, like derived); atomic so a stamp is unique even when Updates
// run concurrently (parallel tests sharing a Model), though the app runs them on one
// goroutine.
type frameClock struct {
	issued, primed atomic.Uint64
}

// stamp marks the cache as final for the Model Update is about to return, and gives
// that Model the stamp; primed then names it. Unique per Update, so no other copy of
// the Model (an older one, a sibling Update's) carries it.
func (c *frameClock) stamp() uint64 {
	n := c.issued.Add(1)
	c.primed.Store(n)
	return n
}

// isPrimed reports whether frame is the stamp the cache was last made final for.
func (c *frameClock) isPrimed(frame uint64) bool {
	return c != nil && frame != 0 && c.primed.Load() == frame
}

// scratch is a private cache for laying out a Model the shared one was not primed
// for, seeded with the room's color slots so names keep their hues.
func (d *derivedCache) scratch() *derivedCache {
	s := newDerivedCache()
	if d != nil {
		s.slots, s.slotsRoom, s.next = maps.Clone(d.slots), d.slotsRoom, d.next
	}
	return s
}

// derivedFor returns the cached answers for the current message set, computing
// them when the set has changed since the last call.
func (m Model) derivedFor() *derivedCache {
	d := m.derived
	if d == nil {
		// A Model built without New still has to render.
		return m.computeDerived(newDerivedCache())
	}
	key := m.keyFor()
	if d.valid && d.key == key {
		return d
	}
	d.key = key
	return m.computeDerived(d)
}

// computeDerived does the three full-history passes and stores them on d.
func (m Model) computeDerived(d *derivedCache) *derivedCache {
	d.claimRoom(m.openRoom)
	d.main, d.threads = m.collapse()
	d.shown = d.main
	if m.thread.open() {
		d.shown, d.threads = domain.ThreadMessages(m.timeline.messages, m.thread.root), nil
	}
	d.shown = m.withDeletionsHidden(d.shown, d.threads)
	d.byAnchor = make(map[domain.EventID][]domain.Thread, len(d.threads))
	for i := range d.threads {
		d.byAnchor[d.threads[i].Anchor] = append(d.byAnchor[d.threads[i].Anchor], d.threads[i])
	}
	d.index = make(map[domain.EventID]int, len(d.shown))
	d.loaded = make(map[domain.EventID]int, len(m.timeline.messages))
	for i := range m.timeline.messages {
		if id := m.timeline.messages[i].ID; id != "" {
			d.loaded[id] = i
		}
	}
	d.nameW = 0
	d.hasMedia = false
	// A name's width depends only on who, under what name, in which room: measured
	// once per sender, not once per message.
	type nameOf struct {
		sender, name string
		room         domain.RoomID
	}
	measured := make(map[nameOf]bool)
	for i := range d.shown {
		msg := &d.shown[i]
		if msg.ID != "" {
			d.index[msg.ID] = i
		}
		if msg.Media != nil {
			d.hasMedia = true
		}
		key := nameOf{msg.Sender, msg.SenderName, msg.RoomID}
		if measured[key] {
			continue
		}
		measured[key] = true
		if w := ansi.StringWidth(m.processedName(*msg)); w > d.nameW {
			d.nameW = w
		}
	}
	d.colors = m.assignColors(d, d.shown)
	// Drop every row explicitly: two timelines can share a length and width.
	d.rowCache, d.rowWidth = nil, -1
	d.valid = true
	return d
}

// rowEntry is one message's rendered rows, and the inputs they were drawn from.
type rowEntry struct {
	rows []string
	// divider: a date divider belongs above (not in rows, so a message's span
	// excludes it).
	divider bool
	// unread: the unread line belongs above, replacing the date divider if both
	// apply (see unreadRow).
	unread bool
	valid  bool
	// height is len(rows) plus any rule above.
	height int

	// in is what the rows read besides the message; a change to any re-renders it.
	in rowInputs
}

// rowInputs is everything one message's rows read that can change while the message
// does not. One comparable struct, so an input is added in one place and compared by
// ==: a key kept field by field is how three audits found inputs it missed. What the
// whole timeline reads (theme, width, the room's place) is in derivedKey instead.
// TestTheRowRendererReadsOnlyKeyedState holds the list against the renderer's reads.
type rowInputs struct {
	// reactions fingerprints the chips drawn (key and count), not how many there are:
	// a swap at the same count must re-render.
	reactions uint64
	// images is the picture rows drawn: they are replaced whole, never edited, so the
	// slice's first element and length name them.
	images  *string
	imagesN int
	threads int
	starred bool
	// revealed: a spoiler shown.
	revealed bool
	// quoted: the reply target is known (loaded or fetched).
	quoted bool
	// pictures: pictures are drawn now (showsPictures, which reads the focus).
	pictures bool
}

// ruleAbove is which horizontal rule, if any, belongs above a message; at most one
// is drawn (see unreadRow).
type ruleAbove struct {
	date   bool
	unread bool
}

// any reports whether a rule is drawn above the message.
func (r ruleAbove) any() bool { return r.date || r.unread }

// entryFor is message i's cached unselected rows, re-rendered if an input moved.
// Selection never changes height, so the totals stay valid as the cursor moves.
func (m Model) entryFor(w walk, i int) *rowEntry {
	d := w.derived
	e := &d.rowCache[i]
	if e.current(m.entryInputs(w, i)) {
		return e
	}
	want := m.renderEntry(w, i)
	// A height change invalidates the running totals from here down.
	if e.valid && e.height != want.height {
		d.dropSuffixAt(i)
	}
	*e = want
	return e
}

// renderEntry is message i's entry drawn from scratch, keyed by its inputs.
func (m Model) renderEntry(w walk, i int) rowEntry {
	msg := w.msgs[i]
	e := rowEntry{in: m.entryInputs(w, i)}
	e.divider = dayKey(msg.Timestamp) != "" &&
		(i == 0 || dayKey(msg.Timestamp) != dayKey(w.msgs[i-1].Timestamp))
	e.unread = m.prefs.display.ShowUnreadLine() && m.timeline.opened.unreadFrom != "" &&
		i > 0 && w.msgs[i-1].ID == m.timeline.opened.unreadFrom
	e.rows = m.renderRows(w, i, false)
	e.height = len(e.rows)
	if e.divider || e.unread {
		e.height++
	}
	e.valid = true
	return e
}

// rowInputsChanged tells the row cache that a reaction or a picture changed, so the
// messages scrolled past are checked for it (see anchorBelow).
func (m Model) rowInputsChanged() {
	if m.derived != nil {
		m.derived.inputsRev++
	}
}

// entryInputs is what message i's rows depend on besides the message itself.
func (m Model) entryInputs(w walk, i int) rowInputs {
	msg := w.msgs[i]
	id := msg.ID
	in := rowInputs{
		reactions: reactionFingerprint(domain.AggregateReactions(m.timeline.reactions[id], "")),
		threads:   len(w.byAnchor[id]),
		starred:   m.isStarred(id),
		revealed:  m.timeline.revealed[id],
		pictures:  m.showsPictures(),
	}
	if rows := m.pics.rowsFor(id); len(rows) > 0 {
		in.images, in.imagesN = &rows[0], len(rows)
	}
	if msg.ReplyTo != "" {
		_, in.quoted = m.quotedTarget(w.derived, msg.ReplyTo)
	}
	return in
}

// reactionFingerprint is an FNV-1a hash of the reaction chips a row draws.
func reactionFingerprint(tallies []domain.ReactionTally) uint64 {
	h := fnvOffset
	for _, t := range tallies {
		h = fnvMix(h, t.Key)
		h = fnvMix(h, strconv.Itoa(t.Count))
	}
	return h
}

// current reports whether e was rendered from these inputs.
func (e *rowEntry) current(want rowInputs) bool { return e.valid && e.in == want }

// heightOf is how many rows message i occupies, its date divider included.
func (m Model) heightOf(w walk, i int) int { return m.entryFor(w, i).height }

// cachedRows is message i's rows as drawn now: cached, or freshly rendered when
// selected.
func (m Model) cachedRows(w walk, i int) (rows []string, rule ruleAbove) {
	e := m.entryFor(w, i)
	above := ruleAbove{date: e.divider, unread: e.unread}
	if w.highlight && w.msgs[i].ID == w.sel {
		return m.renderRows(w, i, true), above
	}
	return e.rows, above
}

// at is the shown message with this ID, and whether it is loaded at all.
func (d *derivedCache) at(id domain.EventID) (domain.Message, bool) {
	i, ok := d.index[id]
	if !ok {
		return domain.Message{}, false
	}
	return d.shown[i], true
}

// collapse is the thread structure of the loaded messages, with the daemon's
// unread counts folded in.
func (m Model) collapse() ([]domain.Message, []domain.Thread) {
	main, threads := domain.CollapseThreads(m.timeline.messages)
	room, ok := m.currentRoom()
	if !ok {
		return main, threads
	}
	// The daemon's counts reflect receipts from other clients too.
	return main, domain.WithUnread(threads, m.unread[room.ID])
}

// assignColors gives every sender in msgs a color by identity group: a pinned color
// when configured, else the hue of the group's slot.
func (m Model) assignColors(d *derivedCache, msgs []domain.Message) map[string]color.Color {
	group := make(map[string]string)       // MXID → group key
	pinned := make(map[string]color.Color) // group key → pinned color
	fresh := make([]string, 0)             // keys seen here that have no slot yet
	for i := range msgs {
		mxid := msgs[i].Sender
		if mxid == "" {
			continue
		}
		if _, ok := group[mxid]; ok {
			continue
		}
		key := mxid
		if id, ok := m.prefs.identities[mxid]; ok {
			key = id.key
			if id.pinned {
				pinned[key] = id.color
			}
		}
		group[mxid] = key
		if _, ok := d.slots[key]; !ok && !containsKey(fresh, key) {
			fresh = append(fresh, key)
		}
	}
	// Sorted, so a fresh room's slots do not depend on who spoke first.
	slices.Sort(fresh)
	for _, key := range fresh {
		if _, isPinned := pinned[key]; isPinned {
			continue
		}
		d.slots[key] = d.next
		if d.unsaved == nil {
			d.unsaved = map[string]int{}
		}
		d.unsaved[key] = d.next
		d.next++
	}
	keyColor := m.hues(d, pinned)
	out := make(map[string]color.Color, len(group))
	for mxid, key := range group {
		if c, ok := keyColor[key]; ok {
			out[mxid] = c
		}
	}
	return out
}

// adoptSlots takes the slots stored for this room, which win over local ones; next
// moves past the highest slot seen.
func (d *derivedCache) adoptSlots(room domain.RoomID, stored map[string]int) bool {
	if room == "" || len(stored) == 0 {
		return false
	}
	// Claimed rather than required: the answer often arrives before any color was
	// asked for. The caller has checked this is the open room.
	d.claimRoom(room)
	changed := false
	for key, slot := range stored {
		if was, ok := d.slots[key]; !ok || was != slot {
			d.slots[key] = slot
			changed = true
		}
		delete(d.unsaved, key)
		if slot+1 > d.next {
			d.next = slot + 1
		}
	}
	if changed {
		d.valid = false
	}
	return changed
}

// claimRoom restarts the hue numbering when the room changes.
func (d *derivedCache) claimRoom(room domain.RoomID) {
	if d.slotsRoom != room {
		d.slotsRoom, d.slots, d.unsaved, d.next = room, map[string]int{}, nil, 0
	}
}

// takeUnsaved returns the slots awaiting a write and forgets them, so a failing
// store costs color memory rather than an unbounded retry queue.
func (d *derivedCache) takeUnsaved() (domain.RoomID, map[string]int) {
	if len(d.unsaved) == 0 {
		return "", nil
	}
	slots := d.unsaved
	d.unsaved = nil
	return d.slotsRoom, slots
}

// hues resolves every slot into a color: pinned ones as configured, the rest spread
// across the wheel clear of the pinned ones (e.g. the green reserved for "me").
func (m Model) hues(d *derivedCache, pinned map[string]color.Color) map[string]color.Color {
	avoid := slices.SortedFunc(maps.Values(pinned), func(a, b color.Color) int {
		return cmp.Compare(avoidOrderKey(a), avoidOrderKey(b))
	})
	spread := m.theme.SpreadColors(d.next, avoid)

	out := make(map[string]color.Color, len(d.slots)+len(pinned))
	maps.Copy(out, pinned)
	for key, slot := range d.slots {
		if _, isPinned := out[key]; isPinned {
			continue
		}
		if slot < len(spread) {
			out[key] = spread[slot]
		} else {
			out[key] = m.theme.Palette.Text
		}
	}
	return out
}

// avoidOrderKey orders colors deterministically for the avoid list.
func avoidOrderKey(c color.Color) uint64 {
	r, g, b, a := c.RGBA()
	return uint64(r)<<48 | uint64(g)<<32 | uint64(b)<<16 | uint64(a)
}
