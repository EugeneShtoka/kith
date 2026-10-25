package domain

import "slices"

// Account is one connected account of a network, as a call that names an account
// takes it.
type Account struct {
	Network Protocol
	// On is its rooms' owner: what a call names the account by.
	On RoomOwner
	// Name is what the config calls it (Matrix: the MXID).
	Name string
	// Default is the network's [<network>] default.
	Default bool
}

// PickAccount is the account of network a call that names none goes to: what opening
// a link, joining by address or starting a chat does. in is the accounts already in the
// room or chat it is about, if any. Those come first: one of them is the one; of
// several, the default if it is among them. With none in it, a network's only account
// is the one, else its default. Otherwise ask lists who to ask between; both are empty
// when network has no account.
func PickAccount(accounts []Account, network Protocol, in []RoomOwner) (picked Account, ask []Account) {
	var all, joined []Account
	for _, a := range accounts {
		if a.Network != network {
			continue
		}
		all = append(all, a)
		if slices.Contains(in, a.On) {
			joined = append(joined, a)
		}
	}
	candidates := all
	if len(joined) > 0 {
		candidates = joined
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if i := slices.IndexFunc(candidates, func(a Account) bool { return a.Default }); i >= 0 {
		return candidates[i], nil
	}
	return Account{}, candidates
}
