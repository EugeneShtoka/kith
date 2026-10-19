package tui

import "github.com/EugeneShtoka/kith/internal/domain"

// A chat whose messages go out as SMS or MMS (Room.SMS) goes through the phone's
// carrier, which may charge for each; with [composer] confirm_sms on (the default), a
// message sent or scheduled there asks first. The draft stays in the composer until
// the answer, and a "no" leaves it there.

// asksSMS reports whether sending to room asks first.
func (m Model) asksSMS(room domain.Room) bool {
	return room.SMS && m.conf.base.Composer.ConfirmsSMS()
}

// smsPrompt is the question before sending or scheduling to a chat that sends as SMS.
func (m Model) smsPrompt(c confirmState) string {
	verb := "send to "
	if c.action == pendingScheduleSMS {
		verb = "schedule this for "
	}
	return verb + m.roomName(c.room) + " as SMS? it goes through your phone's carrier"
}
