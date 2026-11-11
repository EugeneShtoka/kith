package tui

import (
	"context"
	"maps"
	"slices"

	tea "charm.land/bubbletea/v2"
)

// People held only as IDs (a tag's people) are named by asking kithd (KnownAs), as
// everything it sends names them already; when it says names changed, every ID asked
// for is asked again.

// knownAsSource is a backend that names people by ID. Optional, like changesSource.
type knownAsSource interface {
	KnownAs(ctx context.Context, ids []string) ([]string, error)
}

// namedMsg is kithd's name for each ID asked for.
type namedMsg struct {
	names map[string]string
	err   error
}

// askNamesCmd asks kithd's name for ids; nil when there are none or the backend cannot.
func (m Model) askNamesCmd(ids []string) tea.Cmd {
	source, ok := m.backend.(knownAsSource)
	if !ok || len(ids) == 0 {
		return nil
	}
	ctx, ids := m.ctx, slices.Clone(ids)
	return func() tea.Msg {
		names, err := source.KnownAs(ctx, ids)
		out := make(map[string]string, len(ids))
		for i, id := range ids {
			if i < len(names) && names[i] != "" {
				out[id] = names[i]
			}
		}
		return namedMsg{names: out, err: err}
	}
}

// handleNamed keeps the names; on a failure the last ones stay.
func (m Model) handleNamed(msg namedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayFailure("could not name people: ", msg.err), nil
	}
	named := maps.Clone(m.named)
	if named == nil {
		named = map[string]string{}
	}
	maps.Copy(named, msg.names)
	m.named = named
	return m, nil
}

// askNamesAgainCmd asks again for every ID asked for before.
func (m Model) askNamesAgainCmd() tea.Cmd {
	return m.askNamesCmd(slices.Sorted(maps.Keys(m.named)))
}
