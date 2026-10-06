package tui

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// :import copies an account's groupings (Telegram's folders) into tags, once, as the
// person chooses per tag: the networks and accounts are the ones set up (as :login
// offers them), the folders what the daemon previews, each with what a copy would
// change among that account's rooms. A folder no tag is named after makes its tag; one
// whose tag exists changes nothing until a choice is made for it.

// groupingsStore is the daemon copying groupings. Optional, as configStore is.
type groupingsStore interface {
	PreviewGroupings(ctx context.Context, network, account string) ([]domain.GroupingDiff, error)
	ApplyGroupings(ctx context.Context, network, account string, choices map[string]domain.GroupingChoice) error
}

var errNoImport = errors.New("this kith copies no folders")

// importState is an :import under way.
type importState struct {
	networks []api.LoginNetwork
	network  api.LoginNetwork
	account  string
	diffs    []domain.GroupingDiff
	choices  map[string]domain.GroupingChoice
	// folder is the folder whose choice is being made.
	folder string
}

// importCopy is the folder list's last row: copy as chosen.
const importCopy = "\x00copy"

// importNetworksMsg is the networks set up, to import from.
type importNetworksMsg struct {
	networks []api.LoginNetwork
	err      error
}

// importPreviewMsg is the daemon's preview of an account's folders; offered when kith
// offers them unasked, an account just set up, where none to copy says nothing.
type importPreviewMsg struct {
	diffs   []domain.GroupingDiff
	err     error
	offered bool
}

// importedMsg is the outcome of the copy.
type importedMsg struct {
	copied int
	err    error
}

// plural is the word for n of it.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// openImport starts :import: the networks set up, to choose from.
func (m Model) openImport(string) (Model, tea.Cmd) {
	logins, ok := m.backend.(interface {
		LoginNetworks(ctx context.Context) ([]api.LoginNetwork, error)
	})
	if _, copies := m.backend.(groupingsStore); !ok || !copies {
		return m.sayErr("could not copy folders", errNoImport), nil
	}
	ctx := m.ctx
	return m, func() tea.Msg {
		networks, err := logins.LoginNetworks(ctx)
		return importNetworksMsg{networks: networks, err: err}
	}
}

// handleAccounts is news of an account: a sign-in's, or an :import's.
func (m Model) handleAccounts(msg tea.Msg) (Model, tea.Cmd) {
	switch msg.(type) {
	case loginMsg, loginNetworksMsg:
		return m.handleLogin(msg)
	}
	return m.handleImport(msg)
}

// handleImport is an :import's news.
func (m Model) handleImport(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case importNetworksMsg:
		return m.handleImportNetworks(msg)
	case importPreviewMsg:
		return m.handleImportPreview(msg)
	case importedMsg:
		if msg.err != nil {
			return m.sayErr("could not copy the folders", msg.err), nil
		}
		return m.say(fmt.Sprintf("copied %d %s into tags", msg.copied, plural(msg.copied, "folder", "folders"))), nil
	}
	return m, nil
}

// handleImportNetworks offers the accounts set up on every network.
func (m Model) handleImportNetworks(msg importNetworksMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not list the networks", msg.err), nil
	}
	m.imports = importState{networks: msg.networks}
	var items []pickerItem
	for _, n := range msg.networks {
		for _, a := range n.Accounts {
			items = append(items, pickerItem{label: n.Label + " " + a.Name, detail: a.Detail, value: n.Network + "\x00" + a.Name})
		}
	}
	switch len(items) {
	case 0:
		return m.say("no account is set up to copy folders from — :login first"), nil
	case 1:
		return m.chooseImportAccount(items[0].value)
	}
	m.picker = newPickerWith(pickerImportAccount, pickerSpecs[pickerImportAccount], items)
	return m, nil
}

// chooseImportAccount previews the account's folders.
func (m Model) chooseImportAccount(value string) (Model, tea.Cmd) {
	m = m.closePicker()
	key, account, _ := strings.Cut(value, "\x00")
	if i := slices.IndexFunc(m.imports.networks, func(n api.LoginNetwork) bool { return n.Network == key }); i >= 0 {
		m.imports.network = m.imports.networks[i]
	}
	m.imports.account = account
	return m.doing("reading " + m.imports.network.Label + " " + account + "'s folders…"), m.previewImportCmd(false)
}

// previewImportCmd has the daemon preview the account's folders.
func (m Model) previewImportCmd(offered bool) tea.Cmd {
	store, ok := m.backend.(groupingsStore)
	if !ok {
		return nil
	}
	ctx, network, account := m.ctx, m.imports.network.Network, m.imports.account
	return func() tea.Msg {
		diffs, err := store.PreviewGroupings(ctx, network, account)
		return importPreviewMsg{diffs: diffs, err: err, offered: offered}
	}
}

// offerImport offers an account just set up its folders, when its network has any to
// copy: making a tag for each folder no tag is named after, keeping the rest until
// chosen, as :import does.
func (m Model) offerImport(network api.LoginNetwork, account string) (Model, tea.Cmd) {
	if account == "" {
		return m, nil
	}
	m.imports = importState{network: network, account: account}
	return m, m.previewImportCmd(true)
}

// handleImportPreview offers the folders, each with its choice: a new tag is made, an
// existing one kept until chosen.
func (m Model) handleImportPreview(msg importPreviewMsg) (Model, tea.Cmd) {
	if msg.offered && (msg.err != nil || len(msg.diffs) == 0) {
		return m, nil // unasked: a network without folders says nothing
	}
	if errors.Is(msg.err, api.ErrNotOnNetwork) {
		return m.say(m.imports.network.Label + " has no folders to copy"), nil
	}
	if msg.err != nil {
		return m.sayErr("could not read the folders", msg.err), nil
	}
	if len(msg.diffs) == 0 {
		return m.say(m.imports.network.Label + " " + m.imports.account + " has no folders"), nil
	}
	choices := map[string]domain.GroupingChoice{}
	for _, d := range msg.diffs {
		if !d.Exists {
			choices[d.Grouping.Name] = domain.TakeNetworks
		}
	}
	m.imports.diffs, m.imports.choices = msg.diffs, choices
	return m.importFolders(), nil
}

// importFolders lists the folders with what each would do, and the row that copies.
func (m Model) importFolders() Model {
	items := make([]pickerItem, 0, len(m.imports.diffs)+1)
	for i, d := range m.imports.diffs {
		items = append(items, pickerItem{label: d.Grouping.Name, detail: m.importDetail(d), value: strconv.Itoa(i)})
	}
	items = append(items, pickerItem{label: "Copy", detail: "as chosen above", value: importCopy})
	spec := pickerSpecs[pickerImportFolders]
	spec.title = m.imports.network.Label + " " + m.imports.account + ": copy which folders into tags?"
	m.picker = newPickerWith(pickerImportFolders, spec, items)
	return m
}

// importDetail says what a folder's copy would do, as chosen.
func (m Model) importDetail(d domain.GroupingDiff) string {
	var what string
	switch c := m.imports.choices[d.Grouping.Name]; {
	case !d.Exists:
		what = fmt.Sprintf("a new tag, %d %s", len(d.Grouping.Rooms), plural(len(d.Grouping.Rooms), "chat", "chats"))
	case len(d.Add)+len(d.Remove) == 0:
		what = "the tag holds these chats already"
	case c == domain.TakeNetworks:
		what = fmt.Sprintf("take %s's: +%d −%d", m.imports.network.Label, len(d.Add), len(d.Remove))
	case c == domain.MergeIn:
		what = fmt.Sprintf("merge: +%d", len(d.Add))
	default:
		what = fmt.Sprintf("keep kith's (differs: +%d −%d) — choose", len(d.Add), len(d.Remove))
	}
	if len(d.Grouping.Left) > 0 {
		what += "; " + strings.Join(d.Grouping.Left, "; ")
	}
	return what
}

// chooseImportFolder offers a folder's choices, or copies on the last row.
func (m Model) chooseImportFolder(value string) (Model, tea.Cmd) {
	m = m.closePicker()
	if value == importCopy {
		return m.copyImport()
	}
	i, err := strconv.Atoi(value)
	if err != nil || i < 0 || i >= len(m.imports.diffs) {
		return m, nil
	}
	d := m.imports.diffs[i]
	if !d.Exists || len(d.Add)+len(d.Remove) == 0 {
		return m.importFolders(), nil // nothing to choose
	}
	m.imports.folder = d.Grouping.Name
	label := m.imports.network.Label
	items := []pickerItem{
		{label: "Take " + label + "'s", detail: fmt.Sprintf("+%d −%d: this account's chats in the tag become the folder's", len(d.Add), len(d.Remove)), value: strconv.Itoa(int(domain.TakeNetworks))},
		{label: "Merge", detail: fmt.Sprintf("+%d: add the folder's chats, take none away", len(d.Add)), value: strconv.Itoa(int(domain.MergeIn))},
		{label: "Keep kith's", detail: "change nothing", value: strconv.Itoa(int(domain.KeepTags))},
	}
	spec := pickerSpecs[pickerImportChoice]
	spec.title = d.Grouping.Name + ": the tag " + isolate(d.Grouping.Name) + " exists"
	m.picker = newPickerWith(pickerImportChoice, spec, items)
	return m, nil
}

// chooseAccountPick is a pick in a picker about accounts: :login's or :import's.
func (m Model) chooseAccountPick(kind pickerKind, value string) (Model, tea.Cmd) {
	if kind == pickerLoginNetwork {
		return m.chooseLoginNetwork(value)
	}
	if kind == pickerLoginAccount {
		return m.chooseLoginAccount(value)
	}
	if kind == pickerImportAccount {
		return m.chooseImportAccount(value)
	}
	if kind == pickerImportFolders {
		return m.chooseImportFolder(value)
	}
	return m.chooseImportChoice(value)
}

// chooseImportChoice keeps a folder's choice and goes back to the list.
func (m Model) chooseImportChoice(value string) (Model, tea.Cmd) {
	m = m.closePicker()
	c, err := strconv.Atoi(value)
	if err == nil {
		choices := maps.Clone(m.imports.choices) // the Model's copies share the old map
		choices[m.imports.folder] = domain.GroupingChoice(c)
		m.imports.choices = choices
	}
	return m.importFolders(), nil
}

// copyImport has the daemon copy the folders as chosen.
func (m Model) copyImport() (Model, tea.Cmd) {
	store, _ := m.backend.(groupingsStore)
	ctx, network, account, choices := m.ctx, m.imports.network.Network, m.imports.account, m.imports.choices
	copied := 0
	for _, c := range choices {
		if c != domain.KeepTags {
			copied++
		}
	}
	if copied == 0 {
		return m.say("nothing to copy"), nil
	}
	return m.doing("copying the folders…"), func() tea.Msg {
		return importedMsg{copied: copied, err: store.ApplyGroupings(ctx, network, account, choices)}
	}
}
