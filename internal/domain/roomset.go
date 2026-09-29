package domain

// RoomSet is which rooms a query reads: every room, or the ones listed. An empty list
// is no rooms, never every room: a set built from a scope that shares nothing must not
// read everything. EveryRoom is the only way to ask for all of them.
type RoomSet struct {
	All bool
	IDs []RoomID
}

// EveryRoom is every room.
func EveryRoom() RoomSet { return RoomSet{All: true} }

// TheseRooms is exactly ids; none when ids is empty.
func TheseRooms(ids []RoomID) RoomSet { return RoomSet{IDs: ids} }

// None reports whether the set reads no room.
func (s RoomSet) None() bool { return !s.All && len(s.IDs) == 0 }
