package domain

import "time"

// ChatExport is a chat a phone exported (WhatsApp's Export chat) as kithd read it, and
// the rooms it may be, the likeliest first.
type ChatExport struct {
	// Upload is the upload holding the file, which the next steps name.
	Upload      string
	Title       string // the file's name
	Messages    int
	First, Last time.Time
	Rooms       []ExportRoom
}

// ExportRoom is a room's case to be where an export goes.
type ExportRoom struct {
	ID RoomID
	// Found is how many of the export's longer messages the room has cached already.
	Found int
	// Named: the file is named after the room.
	Named bool
	// Adds is how many messages an import would add: those older than its oldest cached.
	Adds int
}

// ExportSenders is who wrote an export's messages, matched to a room's members.
type ExportSenders struct {
	Senders []ExportSender
	Members []Member
	Adds    int
}

// ExportSender is one name an export's messages carry.
type ExportSender struct {
	Name     string
	Messages int
	// Chosen is the member they are when it is clear, else "": the name alone.
	Chosen string
	// Likely is up to three members they may be, the likeliest first.
	Likely []Member
}
