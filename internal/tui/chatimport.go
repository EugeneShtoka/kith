package tui

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/filedialog"
)

// :import also brings in a chat a phone exported (WhatsApp's Export chat): the file is
// read by kithd, which offers the chats it may be, then who wrote it among the chat's
// members, each changeable, then caches what is older than the chat's oldest message.

// chatImporter is the daemon importing chat exports; optional, like groupingsStore.
type chatImporter interface {
	PreviewChatImport(ctx context.Context, path, zone string) (domain.ChatExport, error)
	ChatImportSenders(ctx context.Context, upload, zone string, room domain.RoomID) (domain.ExportSenders, error)
	ApplyChatImport(ctx context.Context, upload, zone string, room domain.RoomID, senders map[string]string) (int, error)
}

// The rows that are not a chat or a person: :import's export row, every other chat,
// the import itself, every member, and a name kept as written.
const (
	importExport    = "\x00export"
	exportElsewhere = "\x00elsewhere"
	exportApply     = "\x00import"
	senderElse      = "\x00else"
	senderAsWritten = "\x00name"
)

type exportChosenMsg struct {
	path string
	err  error
}

type exportReadMsg struct {
	export domain.ChatExport
	err    error
}

type exportSendersMsg struct {
	senders domain.ExportSenders
	err     error
}

type chatImportedMsg struct {
	added int
	err   error
}

// chooseExport asks the desktop for the export, as attaching a file does.
func (m Model) chooseExport() (Model, tea.Cmd) {
	m = m.closePicker()
	ctx := m.ctx
	return m.say("choosing the export…"), func() tea.Msg {
		path, err := filedialog.File(ctx, "Import which WhatsApp chat export?")
		return exportChosenMsg{path: path, err: err}
	}
}

// handleExportChosen reads what was chosen, falls back to a typed path when there is
// no chooser, and stays quiet on cancel.
func (m Model) handleExportChosen(msg exportChosenMsg) (Model, tea.Cmd) {
	switch {
	case errors.Is(msg.err, filedialog.ErrCanceled):
		return m.clearStatus(), nil
	case errors.Is(msg.err, filedialog.ErrUnavailable):
		return m.say("no desktop file chooser here — type a path").openPrompt(promptImportExport), nil
	case msg.err != nil:
		return m.sayErr("file chooser", msg.err), nil
	}
	return m.readExport(msg.path)
}

// submitExportPath reads the export at a typed path.
func (m Model) submitExportPath(input string) (Model, tea.Cmd) {
	path, err := expandHome(strings.TrimSpace(input))
	if err != nil || path == "" {
		return m.say("no file to import"), nil
	}
	return m.readExport(path)
}

// readExport has kithd read the export, in this machine's zone.
func (m Model) readExport(path string) (Model, tea.Cmd) {
	store, ok := m.backend.(chatImporter)
	if !ok {
		return m.sayErr("could not import the chat", errNoImport), nil
	}
	ctx, zone := m.ctx, localZone()
	m.imports.zone = zone
	return m.doing("reading the export…"), func() tea.Msg {
		e, err := store.PreviewChatImport(ctx, path, zone)
		return exportReadMsg{export: e, err: err}
	}
}

// localZone is this machine's zone by IANA name, which the phone that exported a chat
// most likely wrote its times in; "" when it has none to name, for kithd's own.
func localZone() string {
	tz := strings.TrimPrefix(os.Getenv("TZ"), ":")
	if tz == "" {
		tz, _ = os.Readlink("/etc/localtime") // none: no zone to name
	}
	if _, zone, ok := strings.Cut(tz, "zoneinfo/"); ok {
		return zone
	}
	if strings.HasPrefix(tz, "/") {
		return ""
	}
	return tz
}

// exportRooms offers the chats the export may be and a row for any other; all lists
// every chat.
func (m Model) exportRooms(all bool) Model {
	e := m.imports.export
	var items []pickerItem
	if all {
		for i := range m.rooms.all {
			room := m.rooms.all[i]
			items = append(items, pickerItem{label: m.roomName(room), value: string(room.ID), match: m.roomLabel(room) + " " + string(room.ID)})
		}
	} else {
		for _, r := range e.Rooms {
			items = append(items, pickerItem{label: m.roomNameOf(r.ID), detail: exportRoomDetail(r), value: string(r.ID)})
		}
		items = append(items, pickerItem{label: "Somewhere else", detail: "any chat", value: exportElsewhere})
	}
	spec := pickerSpecs[pickerExportRoom]
	spec.title = fmt.Sprintf("Import %s (%d %s, %s – %s) into which chat?", isolate(e.Title), e.Messages,
		plural(e.Messages, "message", "messages"), e.First.Format("2 Jan 2006"), e.Last.Format("2 Jan 2006"))
	m.picker = newPickerWith(pickerExportRoom, spec, items)
	return m
}

// exportRoomDetail says why a chat is offered, and what an import would add to it.
func exportRoomDetail(r domain.ExportRoom) string {
	var parts []string
	if r.Found > 0 {
		parts = append(parts, fmt.Sprintf("has %d of its messages", r.Found))
	}
	if r.Named {
		parts = append(parts, "the file is named after it")
	}
	return strings.Join(append(parts, fmt.Sprintf("adds %d older", r.Adds)), " · ")
}

// chooseExportRoom matches the senders in the chat chosen, or lists every chat.
func (m Model) chooseExportRoom(value string) (Model, tea.Cmd) {
	m = m.closePicker()
	if value == exportElsewhere {
		return m.exportRooms(true), nil
	}
	store, _ := m.backend.(chatImporter)
	room := domain.RoomID(value)
	m.imports.room = room
	ctx, upload, zone := m.ctx, m.imports.export.Upload, m.imports.zone
	return m.doing("matching who wrote it…"), func() tea.Msg {
		s, err := store.ChatImportSenders(ctx, upload, zone, room)
		return exportSendersMsg{senders: s, err: err}
	}
}

// handleExportSenders takes each name as the member kithd is sure of, else as written.
func (m Model) handleExportSenders(msg exportSendersMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not match who wrote it", msg.err), nil
	}
	who := map[string]string{}
	for _, s := range msg.senders.Senders {
		if s.Chosen != "" {
			who[s.Name] = s.Chosen
		}
	}
	m.imports.senders, m.imports.who = msg.senders, who
	return m.exportSenders(), nil
}

// exportSenders lists the export's names with who each is, and the row that imports.
func (m Model) exportSenders() Model {
	senders := m.imports.senders
	items := make([]pickerItem, 0, len(senders.Senders)+1)
	for i, s := range senders.Senders {
		items = append(items, pickerItem{
			label:  isolate(s.Name),
			detail: fmt.Sprintf("%d %s, as %s", s.Messages, plural(s.Messages, "message", "messages"), m.exportWho(s.Name)),
			value:  strconv.Itoa(i),
		})
	}
	items = append(items, pickerItem{label: "Import", value: exportApply,
		detail: fmt.Sprintf("%d older %s into %s", senders.Adds, plural(senders.Adds, "message", "messages"), m.roomNameOf(m.imports.room))})
	spec := pickerSpecs[pickerExportSenders]
	spec.title = "Who wrote it? Choose a name to change who it is"
	m.picker = newPickerWith(pickerExportSenders, spec, items)
	return m
}

// exportWho is who a name is taken as: a member, or the name as written.
func (m Model) exportWho(name string) string {
	id := m.imports.who[name]
	if id == "" {
		return "written, no one's"
	}
	for _, member := range m.imports.senders.Members {
		if member.UserID == id {
			return isolate(m.memberName(member))
		}
	}
	for _, s := range m.imports.senders.Senders {
		for _, member := range s.Likely {
			if member.UserID == id {
				return isolate(m.memberName(member))
			}
		}
	}
	return id
}

// chooseExportSender offers who a name may be, or imports on the last row.
func (m Model) chooseExportSender(value string) (Model, tea.Cmd) {
	m = m.closePicker()
	if value == exportApply {
		return m.applyExport()
	}
	i, err := strconv.Atoi(value)
	if err != nil || i < 0 || i >= len(m.imports.senders.Senders) {
		return m, nil
	}
	m.imports.naming = m.imports.senders.Senders[i].Name
	return m.exportSender(false), nil
}

// exportSender offers who the name being chosen may be: the likeliest members and a
// row for any other, or every member; and the name as written.
func (m Model) exportSender(all bool) Model {
	name := m.imports.naming
	var members []domain.Member
	if all {
		members = m.imports.senders.Members
	} else {
		for _, s := range m.imports.senders.Senders {
			if s.Name == name {
				members = s.Likely
			}
		}
	}
	items := make([]pickerItem, 0, len(members)+2)
	for _, member := range members {
		items = append(items, pickerItem{label: isolate(m.memberName(member)), detail: member.UserID, value: member.UserID})
	}
	if !all {
		items = append(items, pickerItem{label: "Someone else", detail: "any member", value: senderElse})
	}
	items = append(items, pickerItem{label: "As written", detail: "shown as " + isolate(name) + ", no one's", value: senderAsWritten})
	spec := pickerSpecs[pickerExportSender]
	spec.title = isolate(name) + " is who?"
	m.picker = newPickerWith(pickerExportSender, spec, items)
	return m
}

// chooseExportMember keeps who the name is and goes back to the names.
func (m Model) chooseExportMember(value string) (Model, tea.Cmd) {
	m = m.closePicker()
	switch value {
	case senderElse:
		return m.exportSender(true), nil
	case senderAsWritten:
		value = ""
	}
	who := maps.Clone(m.imports.who) // the Model's copies share the old map
	who[m.imports.naming] = value
	m.imports.who = who
	return m.exportSenders(), nil
}

// applyExport has kithd import the chat as chosen.
func (m Model) applyExport() (Model, tea.Cmd) {
	store, _ := m.backend.(chatImporter)
	ctx, upload, zone, room, who := m.ctx, m.imports.export.Upload, m.imports.zone, m.imports.room, m.imports.who
	return m.doing("importing the chat…"), func() tea.Msg {
		added, err := store.ApplyChatImport(ctx, upload, zone, room, who)
		return chatImportedMsg{added: added, err: err}
	}
}

// handleChatExport is news of a chat export's import.
func (m Model) handleChatExport(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case exportChosenMsg:
		return m.handleExportChosen(msg)
	case exportReadMsg:
		if msg.err != nil {
			return m.sayErr("could not read the export", msg.err), nil
		}
		m.imports.export = msg.export
		return m.exportRooms(false), nil
	case exportSendersMsg:
		return m.handleExportSenders(msg)
	case chatImportedMsg:
		if msg.err != nil {
			return m.sayErr("could not import the chat", msg.err), nil
		}
		return m.say(fmt.Sprintf("imported %d %s into %s", msg.added, plural(msg.added, "message", "messages"), m.roomNameOf(m.imports.room))), nil
	}
	return m, nil
}
