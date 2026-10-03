package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
)

// editingTag opens settings → Tags… → the named tag.
func editingTag(t *testing.T, name string) (Model, string) {
	t.Helper()
	m, path := opened(t, config.Notifications{})
	m = pickLabel(t, m, "Tags")
	if m.picker.kind != pickerTags {
		t.Fatalf("Tags… opened picker %v", m.picker.kind)
	}
	m = pickLabel(t, m, name)
	if m.picker.kind != pickerTagEdit || m.choosing.tag.tag != name {
		t.Fatalf("choosing %s opened picker %v on %q", name, m.picker.kind, m.choosing.tag.tag)
	}
	return m, path
}

// Renaming a tag in the editor renames it everywhere it is named, in the running
// config and the saved one, and the editor stays on it.
func TestRenamingATagFromTheEditor(t *testing.T) {
	t.Parallel()
	m, path := editingTag(t, "Pinned")
	cfg := m.conf.base.Clone()
	cfg.Notifications.Rules = append(cfg.Notifications.Rules, config.Rule{Match: "tag:Pinned", Show: "all"})
	cfg.Display.Rail.Order = []string{"tag:Pinned", "*"}
	m, _ = m.applyConfig(cfg, "", "")
	m = m.tagOpen("Pinned")

	m = pickLabel(t, m, "Name")
	m = typeIn(t, m, "Followed")
	if m.configTag("Pinned") >= 0 || m.configTag("Followed") < 0 {
		t.Fatalf("tags = %+v, want Pinned renamed Followed", m.conf.base.Tags)
	}
	if !slices.Contains(m.conf.base.Display.Rail.Order, "tag:Followed") || slices.Contains(m.conf.base.Display.Rail.Order, "tag:Pinned") {
		t.Errorf("rail order = %v, want the new name in it", m.conf.base.Display.Rail.Order)
	}
	if got := m.conf.base.Notifications.Rules[len(m.conf.base.Notifications.Rules)-1].Match; got != "tag:Followed" {
		t.Errorf("the rule names %q, want tag:Followed", got)
	}
	if m.picker.kind != pickerTagEdit || m.choosing.tag.tag != "Followed" {
		t.Errorf("after renaming: picker %v on %q, want the editor on Followed", m.picker.kind, m.choosing.tag.tag)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Tags[m.configTag("Followed")].Name != "Followed" {
		t.Error("the rename was not saved")
	}
}

// A property row flips its property; counts_unread goes back to unset when on.
func TestTagPropertiesToggleFromTheEditor(t *testing.T) {
	t.Parallel()
	m, _ := editingTag(t, "Pinned")
	m = pickLabel(t, m, "Hidden from")
	if !m.conf.base.Tags[m.configTag("Pinned")].Hidden {
		t.Error("hidden did not turn on")
	}
	m = pickLabel(t, m, "Counts as unread")
	if c := m.conf.base.Tags[m.configTag("Pinned")].CountsUnread; c == nil || *c {
		t.Errorf("counts_unread = %v, want false", c)
	}
	m = pickLabel(t, m, "Counts as unread")
	if c := m.conf.base.Tags[m.configTag("Pinned")].CountsUnread; c != nil {
		t.Errorf("counts_unread = %v, want unset (on)", *c)
	}
}

// Rule terms are added, edited and removed one at a time; a term that names nothing
// is refused and changes nothing.
func TestTagRuleEntriesFromTheEditor(t *testing.T) {
	t.Parallel()
	m, _ := editingTag(t, "Pinned")
	m = pickLabel(t, m, "Rule")
	if m.picker.kind != pickerTagEntries {
		t.Fatalf("Rule opened picker %v", m.picker.kind)
	}
	m = pickLabel(t, m, "Add a term")
	m = typeIn(t, m, "space:Work")
	if got := m.conf.base.Tags[m.configTag("Pinned")].Rule; !slices.Equal(got, []string{"space:Work"}) {
		t.Fatalf("rule = %v, want the added term", got)
	}
	if !admitsRoom(m, railRow(t, m, pinnedGroupKey), "!standup:x") {
		t.Error("the rule's room is not in the tag's row")
	}
	m = pickLabel(t, m, "Add a term")
	m = typeIn(t, m, "unred")
	if !strings.Contains(m.status(), "could not apply") || len(m.conf.base.Tags[m.configTag("Pinned")].Rule) != 1 {
		t.Errorf("status %q, rule %v: want a bad term refused", m.status(), m.conf.base.Tags[m.configTag("Pinned")].Rule)
	}
	m = pickLabel(t, m, "space:Work")
	m = typeIn(t, m, "")
	if got := m.conf.base.Tags[m.configTag("Pinned")].Rule; len(got) != 0 {
		t.Errorf("rule = %v, want the emptied term removed", got)
	}
	if m.picker.kind != pickerTagEntries {
		t.Errorf("after editing: picker %v, want the list back", m.picker.kind)
	}
}

// A new tag is made by name and opens in the editor.
func TestMakingATag(t *testing.T) {
	t.Parallel()
	m, _ := opened(t, config.Notifications{})
	m = pickLabel(t, m, "Tags")
	m = pickLabel(t, m, "New tag")
	m = typeIn(t, m, "Family")
	if m.configTag("Family") < 0 || m.choosing.tag.tag != "Family" || m.picker.kind != pickerTagEdit {
		t.Errorf("tags %+v, editor on %q: want Family made and open", m.conf.base.Tags, m.choosing.tag.tag)
	}
	m = m.tagsOpen()
	m = pickLabel(t, m, "New tag")
	m = typeIn(t, m, "family")
	if n := len(m.conf.base.Tags); !strings.Contains(m.status(), "twice") || n != len(starter(t).Tags)+1 {
		t.Errorf("status %q with %d tags: want a second Family refused", m.status(), n)
	}
}

// Deleting asks first, takes the tag out of the rail's lists, and is refused while a
// rule still names it, saying so.
func TestDeletingATagFromTheEditor(t *testing.T) {
	t.Parallel()
	m, _ := editingTag(t, "Pinned")
	m = pickLabel(t, m, "Delete")
	if !m.confirm.active() || !strings.Contains(m.confirmPrompt(), "Pinned") {
		t.Fatalf("delete did not ask first: %q", m.confirmPrompt())
	}
	m, _ = m.resolveConfirm(true)
	if m.configTag("Pinned") >= 0 || slices.Contains(m.conf.base.Display.Rail.Order, "tag:Pinned") {
		t.Errorf("tags %+v, order %v: want Pinned gone from both", m.conf.base.Tags, m.conf.base.Display.Rail.Order)
	}

	cfg := m.conf.base.Clone()
	cfg.Notifications.Rules = append(cfg.Notifications.Rules, config.Rule{Match: "tag:Drafts", Show: "all"})
	m, _ = m.applyConfig(cfg, "", "")
	m.confirm = confirmState{action: pendingDeleteTag, group: "Drafts"}
	m, _ = m.resolveConfirm(true)
	if m.configTag("Drafts") < 0 || !strings.Contains(m.status(), "tag:Drafts") {
		t.Errorf("status %q: want Drafts kept, and the rule naming it said", m.status())
	}
}

// What takes rooms out of a tag's row is shown: every exclusive tag for one that is
// not, only the earlier ones for one that is.
func TestTheEditorSaysWhatAlsoExcludes(t *testing.T) {
	t.Parallel()
	m, _ := editingTag(t, "Unread")
	row := rowFor(t, m, "Also excludes")
	for _, tag := range []string{"Archived", "Spam", "Invites"} {
		if !strings.Contains(row.detail, tag) {
			t.Errorf("Unread also excludes %q, want %s named", row.detail, tag)
		}
	}
	first := m.tagOpen("Spam")
	if row := rowFor(t, first, "Also excludes"); !strings.Contains(row.detail, "Archived") || strings.Contains(row.detail, "Invites") {
		t.Errorf("Spam also excludes %q, want the exclusive tags before it alone", row.detail)
	}
}
