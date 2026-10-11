package domain

import "strconv"

// VerificationKind is the stage of a device-verification flow.
type VerificationKind int

const (
	// VerificationRequested: another device asked to verify this one.
	VerificationRequested VerificationKind = iota
	// VerificationSAS: a short authentication string is ready to compare.
	VerificationSAS
	// VerificationDone: both devices verified each other successfully.
	VerificationDone
	// VerificationCanceled: the flow ended without verifying.
	VerificationCanceled
	// VerificationRestored: room keys gossiped from a verified device were imported
	// (Reason summarizes).
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

// Verification is one step of a SAS device-verification flow, streamed to the UI.
type Verification struct {
	Kind  VerificationKind
	TxnID string

	// Requested: who is asking to verify.
	From   string
	Device string

	// SAS: the string to compare, as emojis and/or the decimal fallback.
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
