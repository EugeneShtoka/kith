package domain

// RoomMerge is a room found to be another: a WhatsApp direct chat keyed by the
// person's LID until its number was learned.
// From's messages and state join Into, and a later write naming From is filed under
// Into. FromPerson and IntoPerson are the person the chat is with, by each ID; their
// verdicts join too ("" for none).
type RoomMerge struct {
	From, Into             RoomID
	FromPerson, IntoPerson string
}

// MergedPeople is the person each merge of merged (a room to the room it joined) moves
// to, by each ID as RoomMerge's FromPerson and IntoPerson name them: a direct chat's
// native ID is the person's (a LID's chat, the LID).
func MergedPeople(merged map[RoomID]RoomID) map[string]string {
	out := map[string]string{}
	for from, into := range merged {
		f, i := ParseID(string(from)), ParseID(string(into))
		if f.Network != "" && i.Network == f.Network {
			out[NativePerson(f.Network, f.Native)] = NativePerson(i.Network, i.Native)
		}
	}
	return out
}
