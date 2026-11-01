package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/phonebook"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// The phonebook for a client that does not share its folder (the desktop): kithd reads
// its own [phonebook] dir afresh on every call, as the TUI does, and writes one key of a
// card over the card as the client read it (phonebook.Book.Write). A client names a
// person by an ID, by a card People listed (its name, looked up among the folder's
// cards, never made a path), or by a room's one person. The request's profile sees no
// one its people.exclude hides: they are not listed, and their card is neither read nor
// written.

// cardRoomsLimit bounds the rooms looked at per ID for a conversation with someone, as
// the TUI's switcher looks (personRoomsLimit); peerMembersLimit the members read of a
// room to find its one person (the TUI's).
const (
	cardRoomsLimit   = 10
	peerMembersLimit = 8
)

// phonebookNow is the phonebook as one request sees it: the folder's cards read now,
// and whom the request's profile hides among them.
type phonebookNow struct {
	book       *phonebook.Book
	index      phonebook.Index
	unreadable []phonebook.Unreadable
	dir        domain.Directory
	groups     [][]string
	selves     []string
	profile    domain.Profile
	// hidden is the profile's people.exclude over these cards (as the cache hides
	// senders by it); terms its field terms, met by a card itself.
	hidden domain.HiddenPeople
	terms  []phonebook.Term
}

// readPhonebook reads the config, the folder, the directory and your IDs for one request.
func (s *server) readPhonebook(ctx context.Context) (phonebookNow, error) {
	snap, err := s.Config.Read()
	if err != nil {
		return phonebookNow{}, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	cfg := snap.Config
	_, _, groups, err := setup.HiddenInputs(cfg)
	if err != nil {
		return phonebookNow{}, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	profile, err := s.Notifications.Profile(domain.ProfileIn(ctx))
	if err != nil {
		return phonebookNow{}, rpcErr(err)
	}
	book := phonebook.New(setup.PhonebookDir(cfg))
	cards, unreadable, err := book.Scan()
	if err != nil {
		return phonebookNow{}, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	dir, err := s.Backend.Directory(ctx)
	if err != nil {
		return phonebookNow{}, rpcErr(err)
	}
	selves, err := s.Backend.Selves(ctx)
	if err != nil {
		return phonebookNow{}, rpcErr(err)
	}
	now := phonebookNow{book: book, index: phonebook.NewIndex(cards), unreadable: unreadable, dir: dir, groups: groups, selves: selves, profile: profile}
	now.hidden = domain.NewExcludedPeople(setup.ProfileExcluded(profile.People, now.index), groups, dir, selves)
	for _, field := range profile.People.Fields {
		if term, ok := phonebook.ParseTerm(field); ok {
			now.terms = append(now.terms, term)
		}
	}
	return now, nil
}

// linked is every ID kith knows id's person by: the directory's and the identity
// groups' links, followed until nothing new turns up.
func (p phonebookNow) linked(ids ...string) []string {
	var out []string
	next := slices.Clone(ids)
	for len(next) > 0 {
		at := next[0]
		next = next[1:]
		if at == "" || slices.Contains(out, at) {
			continue
		}
		out = append(out, at)
		next = append(next, p.dir.Identifiers(at)...)
		for _, group := range p.groups {
			if slices.Contains(group, at) {
				next = append(next, group...)
			}
		}
	}
	return out
}

// cardOf is id's person's card: the first, by file name, holding an identifier of any
// ID linked to theirs.
func (p phonebookNow) cardOf(id string) (phonebook.Card, bool) {
	return p.index.Find(phonebook.Identifiers(domain.CardIdentifiers(p.linked(id))))
}

// isCard reports whether id's person's card is card.
func (p phonebookNow) isCard(id string, card phonebook.Card) bool {
	found, ok := p.cardOf(id)
	return ok && found.Path == card.Path
}

// hides reports whether the profile hides the person on card: the card meets one of
// its field terms, or is the card of a person it names or hides by any ID. Never yours.
func (p phonebookNow) hides(card phonebook.Card) bool {
	if slices.ContainsFunc(p.selves, func(id string) bool { return p.isCard(id, card) }) {
		return false
	}
	if slices.ContainsFunc(p.terms, card.Holds) || slices.ContainsFunc(p.profile.People.IDs, func(id string) bool { return p.isCard(id, card) }) {
		return true
	}
	return slices.ContainsFunc(p.linked(domain.CardPeople(card.IDs)...), p.hidden.Has)
}

// hidesPerson reports whether the profile hides the person id is, or their card.
func (p phonebookNow) hidesPerson(id string) bool {
	if p.hidden.Has(id) {
		return true
	}
	card, ok := p.cardOf(id)
	return ok && p.hides(card)
}

// named is the card People listed as name: one of the folder's, looked up by its name,
// never opened by it.
func (p phonebookNow) named(name string) (phonebook.Card, error) {
	cards := p.index.Cards()
	if at := slices.IndexFunc(cards, func(c phonebook.Card) bool { return c.Name == name }); at >= 0 {
		return cards[at], nil
	}
	if at := slices.IndexFunc(p.unreadable, func(u phonebook.Unreadable) bool { return u.Name == name }); at >= 0 && p.profile.People.Empty() {
		return phonebook.Card{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("daemon: %s.md: %w", name, p.unreadable[at].Err))
	}
	return phonebook.Card{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("daemon: the phonebook has no card %q", name))
}

// people is every ID the person on card, or asked for as id, goes by: what they said is
// searched by these.
func (p phonebookNow) people(card phonebook.Card, id string) []string {
	var out []string
	for _, other := range p.linked(append(domain.CardPeople(card.IDs), id)...) {
		if domain.IsUserID(other) {
			out = append(out, other)
		}
	}
	slices.Sort(out)
	return out
}

// unreadableCards is the cards the folder holds that cannot be read, for a profile that
// hides nobody: whom one is about cannot be judged.
func (p phonebookNow) unreadableCards() []*v1.UnreadableCard {
	if !p.profile.People.Empty() {
		return nil
	}
	out := make([]*v1.UnreadableCard, 0, len(p.unreadable))
	for _, u := range p.unreadable {
		out = append(out, &v1.UnreadableCard{Card: u.Name, Reason: u.Err.Error()})
	}
	return out
}

// People lists the cards the request's profile shows, narrowed by the query.
func (s *server) People(ctx context.Context, r *req[v1.PeopleRequest]) (*resp[v1.PeopleResponse], error) {
	now, err := s.readPhonebook(ctx)
	if err != nil {
		return nil, err
	}
	q := phonebook.ParseQuery(r.Msg.GetQuery())
	out := &v1.PeopleResponse{Unreadable: now.unreadableCards()}
	for _, card := range now.index.Cards() {
		if !card.Matches(q) || now.hides(card) {
			continue
		}
		listed := &v1.PersonListed{Card: card.Name, Aliases: card.Field("aliases")}
		for _, key := range phonebook.IdentifierKeys {
			if len(card.IDs[key]) > 0 {
				listed.IdKeys = append(listed.IdKeys, key)
			}
		}
		for _, t := range q.Terms {
			if f := fieldOf(card, t.Key); f != nil && !slices.ContainsFunc(listed.GetMatched(), func(m *v1.CardField) bool { return m.GetKey() == f.GetKey() }) {
				listed.Matched = append(listed.Matched, f)
			}
		}
		out.People = append(out.People, listed)
	}
	return connect.NewResponse(out), nil
}

// fieldOf is the card's key, in any case, as the card spells and holds it; nil when it
// has none.
func fieldOf(card phonebook.Card, key string) *v1.CardField {
	for _, k := range phonebook.IdentifierKeys {
		if strings.EqualFold(k, key) && len(card.IDs[k]) > 0 {
			return &v1.CardField{Key: k, Values: card.IDs[k], List: true}
		}
	}
	for _, f := range card.Fields {
		if strings.EqualFold(f.Key, key) {
			return &v1.CardField{Key: f.Key, Values: f.Values, List: f.List}
		}
	}
	return nil
}

// cardToProto is a card for the wire; the zero card is a person with none.
func cardToProto(card phonebook.Card) *v1.PhonebookCard {
	out := &v1.PhonebookCard{Card: card.Name, Revision: card.Revision}
	for _, key := range phonebook.IdentifierKeys {
		if values := card.IDs[key]; len(values) > 0 {
			out.Ids = append(out.Ids, &v1.CardField{Key: key, Values: values, List: true})
		}
	}
	for _, f := range card.Fields {
		out.Fields = append(out.Fields, &v1.CardField{Key: f.Key, Values: f.Values, List: f.List})
	}
	return out
}

// whose is the person a PersonCard request names: by ID, by a card People listed, or as
// a room's one person (with the name the room gives them). A person the profile hides is
// refused as one it does not hold.
type whose struct {
	person, name string
	card         phonebook.Card
}

func (s *server) whose(ctx context.Context, now phonebookNow, person, card string, room domain.RoomID) (whose, error) {
	set := 0
	for _, named := range []string{person, card, string(room)} {
		if named != "" {
			set++
		}
	}
	if set != 1 {
		return whose{}, connect.NewError(connect.CodeInvalidArgument, errors.New("daemon: name one person: an ID, a card, or a room"))
	}
	var w whose
	switch {
	case card != "":
		found, err := now.named(card)
		if err != nil {
			return whose{}, err
		}
		if now.hides(found) {
			return whose{}, notInProfile("card", card, now.profile.Name)
		}
		return whose{card: found}, nil
	case room != "":
		member, err := s.roomPerson(ctx, now, room)
		if err != nil {
			return whose{}, err
		}
		w = whose{person: member.UserID, name: member.DisplayName}
	default:
		w = whose{person: person}
	}
	if now.hidesPerson(w.person) {
		return whose{}, notInProfile("person", w.person, now.profile.Name)
	}
	w.card, _ = now.cardOf(w.person)
	return w, nil
}

// roomPerson is the room's one member who is neither you nor a bridge bot.
func (s *server) roomPerson(ctx context.Context, now phonebookNow, room domain.RoomID) (domain.Member, error) {
	members, err := s.Backend.Members(ctx, room, peerMembersLimit)
	if err != nil {
		return domain.Member{}, rpcErr(err)
	}
	var peer domain.Member
	for _, m := range members {
		if slices.Contains(now.selves, m.UserID) || domain.IsBridgeBot(m.UserID) {
			continue
		}
		if peer.UserID != "" || len(members) >= peerMembersLimit {
			return domain.Member{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("daemon: %s is not a room with one person in it", room))
		}
		peer = m
	}
	if peer.UserID == "" {
		return domain.Member{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("daemon: %s has no one in it but you", room))
	}
	return peer, nil
}

// PersonCard is one person's card as the folder holds it now, with every ID they go by
// and your conversation with them in the request's profile.
func (s *server) PersonCard(ctx context.Context, r *req[v1.PersonCardRequest]) (*resp[v1.PersonCardResponse], error) {
	now, err := s.readPhonebook(ctx)
	if err != nil {
		return nil, err
	}
	w, err := s.whose(ctx, now, r.Msg.GetPerson(), r.Msg.GetCard(), domain.RoomID(r.Msg.GetRoomId()))
	if err != nil {
		return nil, err
	}
	people := now.people(w.card, w.person)
	room, err := s.conversation(ctx, now, people)
	if err != nil {
		return nil, err
	}
	out := &v1.PersonCardResponse{Card: cardToProto(w.card), Person: w.person, Name: w.name, People: people, Conversation: string(room)}
	return connect.NewResponse(out), nil
}

// conversation is your DM with the person going by people in the request's profile, or a
// room with them alone, the first RoomsWith lists, as the TUI's switcher finds it.
func (s *server) conversation(ctx context.Context, now phonebookNow, people []string) (domain.RoomID, error) {
	set, err := s.profileSet(ctx, domain.EveryRoom())
	if err != nil {
		return "", rpcErr(err)
	}
	for _, id := range people {
		rooms, err := s.Backend.RoomsWith(ctx, []string{id}, set, cardRoomsLimit)
		if err != nil {
			return "", rpcErr(err)
		}
		for i := range rooms {
			if rooms[i].IsInvite() {
				continue
			}
			if rooms[i].IsDirect {
				return rooms[i].ID, nil
			}
			if peer, err := s.roomPerson(ctx, now, rooms[i].ID); err == nil && slices.Contains(people, peer.UserID) {
				return rooms[i].ID, nil
			}
		}
	}
	return "", nil
}

// SetPersonField writes one key of a person's card over the card as the client read it.
func (s *server) SetPersonField(ctx context.Context, r *req[v1.SetPersonFieldRequest]) (*resp[v1.SetPersonFieldResponse], error) {
	m := r.Msg
	if err := phonebook.CheckField(m.GetKey(), m.GetValues()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	now, err := s.readPhonebook(ctx)
	if err != nil {
		return nil, err
	}
	if m.GetPerson() != "" && m.GetCard() != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("daemon: name one person: an ID or a card"))
	}
	w, err := s.whose(ctx, now, m.GetPerson(), m.GetCard(), "")
	if err != nil {
		return nil, err
	}
	// Over the card as the client read it: one made, changed, renamed or removed since,
	// or another card now theirs, is read again first.
	if w.card.Revision != m.GetRevision() {
		return nil, connect.NewError(connect.CodeAborted, phonebook.ErrMoved)
	}
	ids := phonebook.Identifiers(domain.CardIdentifiers(now.linked(w.person)))
	written, err := now.book.Write(w.card, newCardName(now, w.person, m.GetName()), ids, m.GetKey(), m.GetValues(), m.GetList())
	switch {
	case errors.Is(err, phonebook.ErrMoved):
		return nil, connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, phonebook.ErrMalformed):
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	case err != nil:
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&v1.SetPersonFieldResponse{Card: cardToProto(written)}), nil
}

// newCardName is what a person's new card is named after: the name the client shows
// them by, else the directory's, else their ID's readable part (phonebook.Book.Create
// makes it a file of the folder's own).
func newCardName(now phonebookNow, person, asked string) string {
	if name := strings.TrimSpace(asked); name != "" {
		return name
	}
	if name, _, ok := now.dir.Name(person); ok && name != "" {
		return name
	}
	return domain.ShortName(person)
}

// PersonKeys is every field key the cards the request's profile shows use.
func (s *server) PersonKeys(ctx context.Context, _ *req[v1.PersonKeysRequest]) (*resp[v1.PersonKeysResponse], error) {
	now, err := s.readPhonebook(ctx)
	if err != nil {
		return nil, err
	}
	shown := slices.DeleteFunc(slices.Clone(now.index.Cards()), now.hides)
	return connect.NewResponse(&v1.PersonKeysResponse{Keys: phonebook.NewIndex(shown).Keys()}), nil
}
