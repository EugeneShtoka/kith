package domain

// RoomSet is which rooms a query reads. An empty list is no rooms, so a scope that
// shares nothing reads nothing; only EveryRoom asks for all.
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
