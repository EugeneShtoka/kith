package meta

import (
	"slices"
	"strings"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The inbox is what one connection knows of the account's threads, people and group
// members. Meta never lists it whole: the messages page brings the newest threads, and
// every pushed table adds, renames, moves or deletes some. Each table is applied in
// the order the bridge applies them (people first, then threads, then what is in
// them), and what changed comes back for the cache.

// thread is one Messenger thread as kith keeps it.
type thread struct {
	key    int64
	kind   table.ThreadType
	name   string
	folder string
	// lastActive and readTo are Meta's times in ms: the newest activity and your read
	// mark. readTo only ever moves forward here, as in the cache.
	lastActive, readTo int64
	// unread is Meta's count, -1 when a row did not carry one.
	unread int64
}

// direct reports a one-to-one thread, whose key is the other person's Meta ID.
func (t *thread) direct() bool { return t.kind.IsOneToOne() }

// inbox is one connection's knowledge of its account. Not safe for concurrent use:
// the connection applies tables one at a time.
type inbox struct {
	self    int64
	threads map[int64]*thread
	people  map[int64]string
	members map[int64]map[int64]bool
	// alias is the room key of a thread Meta keys otherwise: an end-to-end encrypted
	// chat's thread has a key of its own, and its chat (whose ID its messages carry) is
	// the other person, or the group's chat ID. Rooms are keyed by the chat.
	alias map[int64]int64
	// left is when you left each chat you left (ms): only newer activity brings it back.
	left map[int64]int64
	// sources are where the attachments of messages not yet cached are (media.go).
	sources map[domain.EventID]mediaSource
}

func newInbox(self int64) *inbox {
	return &inbox{
		self: self, threads: map[int64]*thread{}, people: map[int64]string{},
		members: map[int64]map[int64]bool{}, alias: map[int64]int64{}, left: map[int64]int64{},
		sources: map[domain.EventID]mediaSource{},
	}
}

// key is the room key of Meta's thread key k.
func (in *inbox) key(k int64) int64 {
	if v, ok := in.alias[k]; ok {
		return v
	}
	return k
}

// change is what one table changed.
type change struct {
	// threads is the threads added or changed; gone the ones Meta took away (deleted,
	// left, moved to spam or to end-to-end encryption).
	threads []int64
	gone    []int64
	// people reports new or renamed people.
	people bool
	// read is each thread's read mark that moved forward, in ms.
	read map[int64]int64
	// messages are the table's messages in kept threads, oldest first.
	messages []*table.WrappedMessage
	// left counts the threads not shown, by why.
	left map[string]int
}

func (c *change) touched(key int64) {
	if !slices.Contains(c.threads, key) {
		c.threads = append(c.threads, key)
	}
}

// kept reports why a thread in folder, of kind, is not shown, or "" when it is: spam
// and hidden threads, and Messenger's folders themselves, are not.
func kept(folder string, kind table.ThreadType) string {
	switch {
	case folder == "spam", folder == "hidden":
		return folder
	case kind == table.FOLDER:
		return "folder"
	}
	return ""
}

// apply takes one table into the inbox and reports what changed.
func (in *inbox) apply(tbl *table.LSTable) change {
	c := change{read: map[int64]int64{}, left: map[string]int{}}
	in.applyPeople(tbl, &c)
	in.applyMapping(tbl, &c)
	full := in.applyThreads(tbl, &c)
	in.applyGone(tbl, full, &c)
	in.applyMembers(tbl, &c)
	in.applyRead(tbl, &c)
	in.applyMessages(tbl, &c)
	return c
}

func (in *inbox) know(id int64, name, first string, c *change) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimSpace(first)
	}
	if id == 0 || name == "" || in.people[id] == name {
		return
	}
	in.people[id] = name
	c.people = true
	// A one-to-one thread is named after its person.
	if t := in.threads[id]; t != nil && t.direct() {
		c.touched(id)
	}
}

func (in *inbox) applyPeople(tbl *table.LSTable, c *change) {
	for _, p := range tbl.LSDeleteThenInsertContact {
		in.know(p.Id, p.Name, p.FirstName, c)
	}
	for _, p := range tbl.LSVerifyContactRowExists {
		in.know(p.ContactId, p.Name, p.FirstName, c)
	}
}

// applyThreads applies the thread rows, returning the keys a full row came for (a
// delete of one of those in the same table is Meta's delete-then-insert).
func (in *inbox) applyThreads(tbl *table.LSTable, c *change) map[int64]bool {
	full := map[int64]bool{}
	in.peersFromParticipants(tbl, c)
	for _, r := range tbl.LSDeleteThenInsertThread {
		full[r.ThreadKey] = true
		in.upsert(r.ThreadKey, r.ThreadType, r.ThreadName, r.FolderName, r.LastActivityTimestampMs,
			r.LastReadWatermarkTimestampMs, r.UnreadMessageCount, c)
	}
	for _, r := range tbl.LSUpdateOrInsertThread {
		if full[r.ThreadKey] {
			continue // the full row says it all
		}
		full[r.ThreadKey] = true
		in.upsert(r.ThreadKey, r.ThreadType, r.ThreadName, r.FolderName, r.LastActivityTimestampMs,
			r.LastReadWatermarkTimestampMs, -1, c)
	}
	for _, r := range tbl.LSVerifyThreadExists {
		if t := in.threads[in.key(r.ThreadKey)]; t == nil && !full[r.ThreadKey] {
			in.upsert(r.ThreadKey, r.ThreadType, "", r.FolderName, 0, 0, -1, c)
		}
	}
	for _, r := range tbl.LSSyncUpdateThreadName {
		// A one-to-one thread keeps its person's name, as in Messenger itself.
		if t := in.threads[in.key(r.ThreadKey)]; t != nil && !t.direct() && t.name != r.ThreadName {
			t.name = r.ThreadName
			c.touched(t.key)
		}
	}
	return full
}

// upsert adds or updates a thread from a row, or drops it when the row moves it out of
// what is shown. unread is -1 when the row carries no count.
func (in *inbox) upsert(metaKey int64, kind table.ThreadType, name, folder string, lastActive, readTo, unread int64, c *change) {
	if metaKey == 0 {
		return
	}
	key := in.key(metaKey)
	if at, ok := in.left[key]; ok {
		if lastActive <= at {
			return // from before you left it
		}
		delete(in.left, key)
	}
	if key != metaKey {
		in.retire(metaKey, c)
	}
	t := in.threads[key]
	if t != nil && kind == table.UNKNOWN_THREAD_TYPE {
		kind = t.kind
	}
	if why := kept(folder, kind); why != "" {
		c.left[why]++
		in.drop(key, c)
		return
	}
	if t == nil {
		t = &thread{key: key, unread: -1}
		in.threads[key] = t
	}
	t.kind, t.folder = kind, folder
	if name != "" || !t.direct() {
		t.name = name // a one-to-one thread's is the fallback when its person is not known
	}
	t.lastActive = max(t.lastActive, lastActive)
	if unread >= 0 {
		t.unread = unread
	}
	in.readTo(t, readTo, c)
	c.touched(key)
}

// active moves a chat's newest activity forward to at; false for a chat not shown.
func (in *inbox) active(key, at int64) bool {
	t := in.threads[key]
	if t == nil {
		return false
	}
	t.lastActive = max(t.lastActive, at)
	return true
}

// leave drops the chat keyed key, which you left at at (ms).
func (in *inbox) leave(key, at int64, c *change) {
	in.left[key] = max(in.left[key], at)
	in.drop(key, c)
}

func (in *inbox) drop(key int64, c *change) {
	if in.threads[key] == nil {
		return
	}
	delete(in.threads, key)
	delete(in.members, key)
	c.gone = append(c.gone, key)
	c.threads = slices.DeleteFunc(c.threads, func(k int64) bool { return k == key })
}

// retire forgets a Meta thread key that turned out to be another room's: its room, if
// one was kept under it, goes.
func (in *inbox) retire(metaKey int64, c *change) {
	delete(in.threads, metaKey)
	delete(in.members, metaKey)
	if !slices.Contains(c.gone, metaKey) {
		c.gone = append(c.gone, metaKey)
	}
	c.threads = slices.DeleteFunc(c.threads, func(k int64) bool { return k == metaKey })
}

// rekey keys Meta's thread metaKey by chat from now on.
func (in *inbox) rekey(metaKey, chat int64, c *change) {
	if metaKey == 0 || chat == 0 || metaKey == chat || in.alias[metaKey] == chat {
		return
	}
	in.alias[metaKey] = chat
	if t := in.threads[metaKey]; t != nil {
		in.retire(metaKey, c)
		t.key = chat
		if in.threads[chat] == nil {
			in.threads[chat] = t
		}
		c.touched(chat)
	} else {
		in.retire(metaKey, c)
	}
}

// applyMapping takes Meta's thread-to-chat mappings for encrypted chats.
func (in *inbox) applyMapping(tbl *table.LSTable, c *change) {
	for _, r := range tbl.LSUpdateThreadAuthorityAndMappingWithOTIDFromJID {
		in.rekey(r.ThreadKey, r.ThreadJID, c)
	}
	for _, r := range tbl.LSVerifyHybridThreadExists {
		in.rekey(r.ThreadKey, r.ThreadJID, c)
	}
}

// peersFromParticipants keys an encrypted one-to-one thread by its other person when
// Meta sent no mapping: its participants are you and them.
func (in *inbox) peersFromParticipants(tbl *table.LSTable, c *change) {
	others := map[int64][]int64{}
	for _, r := range tbl.LSAddParticipantIdToGroupThread {
		if r.ContactId != 0 && r.ContactId != in.self {
			others[r.ThreadKey] = append(others[r.ThreadKey], r.ContactId)
		}
	}
	oneToOne := func(key int64, kind table.ThreadType) {
		if kind == table.ENCRYPTED_OVER_WA_ONE_TO_ONE && len(others[key]) == 1 {
			in.rekey(key, others[key][0], c)
		}
	}
	for _, r := range tbl.LSDeleteThenInsertThread {
		oneToOne(r.ThreadKey, r.ThreadType)
	}
	for _, r := range tbl.LSUpdateOrInsertThread {
		oneToOne(r.ThreadKey, r.ThreadType)
	}
}

// readTo moves t's read mark forward to at; never back.
func (in *inbox) readTo(t *thread, at int64, c *change) {
	if at > t.readTo {
		t.readTo = at
		c.read[t.key] = at
	}
}

func (in *inbox) applyGone(tbl *table.LSTable, full map[int64]bool, c *change) {
	for _, r := range tbl.LSDeleteThread {
		if !full[r.ThreadKey] {
			// Deleted on another device is left there: dated now, as Meta says no time.
			in.leave(in.key(r.ThreadKey), time.Now().UnixMilli(), c)
		}
	}
	// A thread moved to end-to-end encryption stays the same room: its messages now come
	// over the encrypted connection, under the same key.
	for _, r := range tbl.LSMoveThreadToE2EECutoverFolder {
		if t := in.threads[in.key(r.ThreadKey)]; t != nil && !t.kind.IsWhatsApp() {
			if t.direct() {
				t.kind = table.ENCRYPTED_OVER_WA_ONE_TO_ONE
			} else {
				t.kind = table.ENCRYPTED_OVER_WA_GROUP
			}
			c.touched(t.key)
		}
	}
}

func (in *inbox) applyMembers(tbl *table.LSTable, c *change) {
	for _, r := range tbl.LSAddParticipantIdToGroupThread {
		key := in.key(r.ThreadKey)
		if in.threads[key] == nil || r.ContactId == 0 {
			continue
		}
		if in.members[key] == nil {
			in.members[key] = map[int64]bool{}
		}
		if !in.members[key][r.ContactId] {
			in.members[key][r.ContactId] = true
			c.touched(key)
		}
		if r.Nickname != "" && in.people[r.ContactId] == "" {
			in.know(r.ContactId, r.Nickname, "", c)
		}
	}
	for _, r := range tbl.LSRemoveParticipantFromThread {
		key := in.key(r.ThreadKey)
		if r.ParticipantId == in.self {
			in.drop(key, c) // you left: it is no longer yours
			continue
		}
		if in.members[key][r.ParticipantId] {
			delete(in.members[key], r.ParticipantId)
			c.touched(key)
		}
	}
}

func (in *inbox) applyRead(tbl *table.LSTable, c *change) {
	for _, r := range tbl.LSMarkThreadReadV2 {
		if t := in.threads[in.key(r.ThreadKey)]; t != nil {
			in.readTo(t, r.LastReadWatermarkTimestampMs, c)
		}
	}
	for _, r := range tbl.LSMarkThreadRead {
		if t := in.threads[in.key(r.ThreadKey)]; t != nil {
			in.readTo(t, r.LastReadWatermarkTimestampMs, c)
		}
	}
}

// applyMessages keeps the table's messages that belong to a shown thread. A message
// for a thread not heard of yet makes a placeholder thread (named once its row comes).
func (in *inbox) applyMessages(tbl *table.LSTable, c *change) {
	upsert, insert := tbl.WrapMessages()
	var msgs []*table.WrappedMessage
	for _, u := range upsert {
		msgs = append(msgs, u.Messages...)
	}
	msgs = append(msgs, insert...)
	for _, m := range msgs {
		if m == nil || m.LSInsertMessage == nil || m.MessageId == "" {
			continue
		}
		m.ThreadKey = in.key(m.ThreadKey)
		t := in.threads[m.ThreadKey]
		if t == nil {
			in.upsert(m.ThreadKey, table.UNKNOWN_THREAD_TYPE, "", "inbox", m.TimestampMs, 0, -1, c)
			if t = in.threads[m.ThreadKey]; t == nil {
				continue
			}
		}
		if m.TimestampMs > t.lastActive {
			t.lastActive = m.TimestampMs
			c.touched(t.key)
		}
		if m.SenderId == in.self {
			// Writing reads the thread, as on every Messenger client.
			in.readTo(t, m.TimestampMs, c)
		}
		c.messages = append(c.messages, m)
	}
	slices.SortStableFunc(c.messages, func(a, b *table.WrappedMessage) int {
		switch {
		case a.TimestampMs < b.TimestampMs:
			return -1
		case a.TimestampMs > b.TimestampMs:
			return 1
		}
		return 0
	})
}

// membersOf is a thread's members other than you: the other person of a one-to-one
// thread, else the group's participants, sorted.
func (in *inbox) membersOf(t *thread) []int64 {
	if t.direct() {
		if t.key == in.self {
			return nil
		}
		return []int64{t.key}
	}
	var out []int64
	for id := range in.members[t.key] {
		if id != in.self {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// unnamed is the people of one-to-one threads among keys whose names are not known.
func (in *inbox) unnamed(keys []int64) []int64 {
	var out []int64
	for _, key := range keys {
		if t := in.threads[key]; t != nil && t.direct() && key != in.self && in.people[key] == "" {
			out = append(out, key)
		}
	}
	return out
}
