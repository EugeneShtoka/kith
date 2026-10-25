package matrix

import (
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// ids converts IDs between Matrix's form, which mautrix and the homeserver speak, and
// kith's, which the cache, the router and every client hold. Every crossing in this
// package goes through it, so what a kith Matrix ID looks like is decided here alone
// (ids_contract_test.go holds the typed crossings to that; an ID read out of a raw
// string, such as a pill or an account-data key, is routed here by hand). kith's form
// names the network and, for a room or an event, the account: "matrix:<account>/!room",
// "matrix:<account>/$event", "matrix:@user:server". An empty ID stays empty both ways:
// it means "none".
type ids struct {
	// account is AccountKey of the account's MXID.
	account string
}

// accountIDs is the seam for the account user (its MXID).
func accountIDs(user string) ids { return ids{account: domain.AccountKey(user)} }

// scoped is a room or event's native ID in kith's form.
func (x ids) scoped(native string) string {
	if native == "" {
		return ""
	}
	return domain.NativeID(domain.ProtocolMatrix, x.account, native)
}

// owner is the account's rooms in the cache.
func (x ids) owner() domain.RoomOwner { return domain.AccountRooms(domain.ProtocolMatrix, x.account) }

// room is a Matrix room ID in kith's form.
func (x ids) room(r id.RoomID) domain.RoomID { return domain.RoomID(x.scoped(string(r))) }

// roomOf is room for a room ID read as a plain string (a state key, account data).
func (x ids) roomOf(native string) domain.RoomID { return x.room(id.RoomID(native)) }

// nativeRoom is a kith room ID in Matrix's form.
func (ids) nativeRoom(r domain.RoomID) id.RoomID { return id.RoomID(domain.MatrixNative(string(r))) }

// event is a Matrix event ID in kith's form.
func (x ids) event(e id.EventID) domain.EventID { return domain.EventID(x.scoped(string(e))) }

// eventOf is event for an event ID read as a plain string (account data).
func (x ids) eventOf(native string) domain.EventID { return x.event(id.EventID(native)) }

// space is a Matrix space's room ID in kith's form.
func (x ids) space(r id.RoomID) domain.SpaceID { return domain.SpaceID(x.scoped(string(r))) }

// nativeSpace is a kith space ID in Matrix's form.
func (ids) nativeSpace(s domain.SpaceID) id.RoomID { return id.RoomID(domain.MatrixNative(string(s))) }

// nativeEvent is a kith event ID in Matrix's form.
func (ids) nativeEvent(e domain.EventID) id.EventID {
	return id.EventID(domain.MatrixNative(string(e)))
}

// user is a Matrix user ID in kith's form.
func (ids) user(u id.UserID) string {
	if u == "" {
		return ""
	}
	return domain.NativePerson(domain.ProtocolMatrix, string(u))
}

// userOf is user for a user ID read as a plain string (a pill, account data).
func (x ids) userOf(native string) string { return x.user(id.UserID(native)) }

// nativeUser is a kith user ID in Matrix's form.
func (ids) nativeUser(u string) id.UserID { return id.UserID(domain.MatrixNative(u)) }

// configuredUser is the account's own MXID as the config or a login names it: Matrix's
// form already, never kith's.
func configuredUser(mxid string) id.UserID { return id.UserID(mxid) }

// matrixString is a Matrix ID as Matrix's own string, for what mautrix or the
// homeserver takes as plain text (a state key, a query parameter, a comparison with
// one). It is never kith's form.
func matrixString[T id.RoomID | id.EventID | id.UserID](v T) string { return string(v) }

// kithRoom is a room ID kith holds as a plain string (a paging token, a mention):
// kith's form already, never Matrix's.
func kithRoom(s string) domain.RoomID { return domain.RoomID(s) }
