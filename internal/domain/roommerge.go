package domain

// RoomMerge is a room found to be another: a WhatsApp direct chat keyed by the
// person's LID until its number was learned (planning/design-mesh.md, decision 17).
// From's messages and state join Into, and a later write naming From is filed under
// Into. FromPerson and IntoPerson are the person the chat is with, by each ID; their
// verdicts join too ("" for none).
type RoomMerge struct {
	From, Into             RoomID
	FromPerson, IntoPerson string
}
