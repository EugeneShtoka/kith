package tui

import (
	"log/slog"
	"maps"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A room or person a network shows only as a number is named from the daemon's phone
// book (domain.PhoneBook): the best name any account or bridge knows the number by.
// Your own names come first: a room's [[display.name]], an identity's alias. The book
// is read again with every room list, since a sync that brings names brings rooms.

// phoneState is the phone book as last read, and a revision that moves when it changes.
type phoneState struct {
	book domain.PhoneBook
	rev  uint64
}

// phoneBookMsg is the daemon's phone book.
type phoneBookMsg struct {
	book domain.PhoneBook
	err  error
}

// phoneBookCmd reads the phone book.
func (m Model) phoneBookCmd() tea.Cmd {
	return fetch(m.ctx, m.backend.PhoneBook, func(book domain.PhoneBook, err error) tea.Msg {
		return phoneBookMsg{book: book, err: err}
	})
}

// handlePhoneBook keeps a phone book that changed; one that could not be read keeps
// the last.
func (m Model) handlePhoneBook(msg phoneBookMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelWarn, "read the phone book", msg.err)
	if msg.err != nil || maps.Equal(msg.book, m.phones.book) {
		return m, nil
	}
	m.phones = phoneState{book: msg.book, rev: m.phones.rev + 1}
	return m.rebuiltRail(), nil
}

// memberName is a member's name, the phone book's when they show only as a number.
func (m Model) memberName(member domain.Member) string { return m.byNumber(member.Name()) }

// byNumber is label, or the phone book's name for it when label is only a number.
func (m Model) byNumber(label string) string {
	if name, ok := m.phones.book.Named(label); ok {
		return name
	}
	return label
}
