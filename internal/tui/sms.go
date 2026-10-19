package tui

import (
	"path/filepath"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A chat whose messages go out as SMS or MMS (Room.SMS) goes through the phone's
// carrier, which may charge for each; with [composer] confirm_sms on (the default), a
// message or file sent, or a message scheduled, there asks first. The draft stays in the composer until
// the answer, and a "no" leaves it there.

// asksSMS reports whether sending to room asks first.
func (m Model) asksSMS(room domain.Room) bool {
	return room.SMS && m.conf.base.Composer.ConfirmsSMS()
}

// smsPrompt is the question before sending or scheduling to a chat that sends as SMS.
func (m Model) smsPrompt(c confirmState) string {
	switch c.action { //nolint:exhaustive // asked only for these
	case pendingScheduleSMS:
		return "schedule this for " + m.roomName(c.room) + " as SMS? it goes through your phone's carrier"
	case pendingAttachSMS:
		return "send " + isolate(filepath.Base(c.body)) + " to " + m.roomName(c.room) + " as MMS? it goes through your phone's carrier"
	}
	return "send to " + m.roomName(c.room) + " as SMS? it goes through your phone's carrier"
}
