package main

import (
	"context"
	"sync"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// callMemo caches what one tool call reads from the daemon repeatedly (the scope check
// reads spaces per room). Per call only, so changes are seen on the next call.
type callMemo struct {
	roomsOnce  sync.Once
	rooms      []domain.Room
	roomsErr   error
	spacesOnce sync.Once
	spaces     []domain.Space
	spacesErr  error
	// plain are rooms the daemon said are unencrypted. Per call: encryption can turn on at
	// any time but never off, so only "encrypted" outlives a call (server.crypto).
	plainMu sync.Mutex
	plain   map[domain.RoomID]bool
}

// memoOf is ctx's call memo, or nil outside a tool call.
func memoOf(ctx context.Context) *callMemo {
	m, _ := ctx.Value(memoKey{}).(*callMemo)
	return m
}

// isPlain reports whether a room is known unencrypted in this call.
func (m *callMemo) isPlain(roomID domain.RoomID) bool {
	if m == nil {
		return false
	}
	m.plainMu.Lock()
	defer m.plainMu.Unlock()
	return m.plain[roomID]
}

func (m *callMemo) notePlain(roomID domain.RoomID) {
	if m == nil {
		return
	}
	m.plainMu.Lock()
	defer m.plainMu.Unlock()
	if m.plain == nil {
		m.plain = map[domain.RoomID]bool{}
	}
	m.plain[roomID] = true
}

type memoKey struct{}

func withCallMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, memoKey{}, &callMemo{})
}

// rooms is the room list, read once per tool call.
func (s *server) rooms(ctx context.Context) ([]domain.Room, error) {
	m, ok := ctx.Value(memoKey{}).(*callMemo)
	if !ok {
		return s.backend.Rooms(ctx) //nolint:wrapcheck // callers wrap with what they were doing
	}
	m.roomsOnce.Do(func() {
		m.rooms, m.roomsErr = s.backend.Rooms(ctx)
		s.learnEncryption(ctx, m.rooms)
	})
	return m.rooms, m.roomsErr
}

// learnEncryption asks in one call about every room not known to be encrypted. A
// failure leaves them unknown, each then asked (and failed closed) alone.
func (s *server) learnEncryption(ctx context.Context, rooms []domain.Room) {
	s.mu.Lock()
	var unknown []domain.RoomID
	for i := range rooms {
		if !s.crypto[rooms[i].ID] {
			unknown = append(unknown, rooms[i].ID)
		}
	}
	s.mu.Unlock()
	if len(unknown) == 0 {
		return
	}
	state, err := s.backend.RoomEncryption(ctx, unknown)
	if err != nil {
		s.logger().Warn("room encryption lookup failed; asking room by room", "rooms", len(unknown), "err", err)
		return
	}
	memo := memoOf(ctx)
	for _, id := range unknown {
		if state[id] {
			s.noteEncrypted(id)
		} else {
			memo.notePlain(id)
		}
	}
}

// noteEncrypted remembers an encrypted room for the process's life: encryption never
// turns off.
func (s *server) noteEncrypted(roomID domain.RoomID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.crypto == nil {
		s.crypto = map[domain.RoomID]bool{}
	}
	s.crypto[roomID] = true
}

// spaces is the space list, read once per tool call.
func (s *server) spaces(ctx context.Context) ([]domain.Space, error) {
	m, ok := ctx.Value(memoKey{}).(*callMemo)
	if !ok {
		return s.backend.Spaces(ctx) //nolint:wrapcheck // callers wrap with what they were doing
	}
	m.spacesOnce.Do(func() { m.spaces, m.spacesErr = s.backend.Spaces(ctx) })
	return m.spaces, m.spacesErr
}
