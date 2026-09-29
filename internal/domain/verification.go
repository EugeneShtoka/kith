package domain

import "strconv"

// VerificationKind identifies which stage of an interactive device-verification
// flow a Verification event represents.
type VerificationKind int

const (
	// VerificationRequested: another device asked to verify this one; the user chooses
	// whether to accept.
	VerificationRequested VerificationKind = iota
	// VerificationSAS: a short authentication string is ready to compare against the
	// other device.
	VerificationSAS
	// VerificationDone: both devices verified each other successfully.
	VerificationDone
	// VerificationCanceled: the flow ended without verifying.
	VerificationCanceled
	// VerificationRestored: after a successful verification, room keys were gossiped
	// from a verified device and imported (Reason carries a summary).
	VerificationRestored
)

// kindNames is each kind's name, for a message that shows one.
var kindNames = map[VerificationKind]string{
	VerificationRequested: "requested",
	VerificationSAS:       "sas",
	VerificationRestored:  "restored",
}

// String names the kind.
func (k VerificationKind) String() string {
	if name, ok := kindNames[k]; ok {
		return name
	}
	return "verification(" + strconv.Itoa(int(k)) + ")"
}

// Verification is one step of an interactive SAS device-verification flow, streamed
// from the backend to the UI.
type Verification struct {
	Kind  VerificationKind
	TxnID string

	// Requested: who is asking to verify.
	From   string
	Device string

	// SAS: the short authentication string to compare — one entry per emoji (glyph +
	// human name) and/or the decimal fallback.
	Emojis   []SASEmoji
	Decimals []int

	// Canceled: a human-readable reason.
	Reason string
}

// SASEmoji is one emoji of a short authentication string: the glyph and its
// canonical name (e.g. "🐶" / "Dog").
type SASEmoji struct {
	Glyph string
	Name  string
}
