package tui

import (
	"fmt"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Offering the local completion model once, when its weights are not installed.
// The config half is checked here; the daemon checks the filesystem because it is
// the process that spawns llama-server. esc dismisses until next launch; unticking
// the row and accepting declines for good ([complete.model] declined = true).

// modelOffer is the state behind the prompt.
type modelOffer struct {
	asked bool   // the offer was already raised this session
	tag   string // what the prompt offers, to tell an unticked row from a ticked one
}

// maybeOfferModel asks the daemon whether there is anything to offer, once.
func (m Model) maybeOfferModel() (Model, tea.Cmd) {
	model := m.conf.base.Complete.Model
	switch {
	case m.offers.model.asked:
		return m, nil
	case !m.conf.base.Complete.CompleteEnabled(), !model.ModelEnabled(), model.Declined:
		return m, nil
	}
	m.offers.model.asked = true
	backend, ctx := m.backend, m.ctx
	return m, func() tea.Msg {
		found, err := backend.DetectModel(ctx)
		if err != nil {
			// Unrequested, so a failure is not news.
			return modelOfferMsg{}
		}
		return modelOfferMsg{suggestion: found}
	}
}

// modelOfferMsg carries the daemon's answer back to the event loop.
type modelOfferMsg struct {
	suggestion domain.ModelSuggestion
}

// handleModelOffer raises the prompt, or says why it cannot.
func (m Model) handleModelOffer(msg modelOfferMsg) (Model, tea.Cmd) {
	if why := msg.suggestion.Why; why != "" {
		// The engine is missing; say so once with the install command.
		return m.say(firstLine(why)), nil
	}
	if !msg.suggestion.Offer {
		return m, nil
	}
	candidate := msg.suggestion.Candidate
	m.offers.model.tag = candidate.Tag
	// Ticked: the prompt lets somebody disagree, not opt in.
	m.picker = newCheckedPicker(pickerCompletionModel,
		[]pickerItem{{
			label:  candidate.Tag,
			value:  candidate.Tag,
			detail: modelOfferDetail(candidate),
		}},
		map[string]bool{candidate.Tag: true})
	return m, nil
}

// modelOfferDetail is the evidence on the row, accuracy first.
func modelOfferDetail(c domain.ModelCandidate) string {
	return fmt.Sprintf("%s · finishes the word you were about to type %.0f%% of the time, in ~20ms · %.0f MB · runs here, over no network",
		c.Name, 100*c.Recall, float64(c.Bytes)/(1<<20))
}

// acceptModelOffer downloads what was ticked and remembers what was not.
func (m Model) acceptModelOffer(tags []string) (Model, tea.Cmd) {
	declined := m.offers.model.tag != "" && !slices.Contains(tags, m.offers.model.tag)

	m = m.closePicker()
	if declined {
		// Only the explicit "not this one" is written down.
		cfg := m.conf.base.Clone()
		cfg.Complete.Model.Declined = true
		return m.applyConfig(cfg, "")
	}
	return m.installModel(tags)
}

// installModel fetches the weights off the event loop, reporting once at the end.
func (m Model) installModel(tags []string) (Model, tea.Cmd) {
	if len(tags) == 0 {
		return m, nil
	}
	tag := tags[0]
	m = m.say("fetching the completion model (369 MB) — it will start answering when it lands…")
	backend, ctx := m.backend, m.ctx
	return m, func() tea.Msg {
		if err := backend.InstallModel(ctx, tag); err != nil {
			return modelInstalledMsg{tag: tag, err: err.Error()}
		}
		return modelInstalledMsg{tag: tag}
	}
}

// modelInstalledMsg is the outcome of installModel.
type modelInstalledMsg struct {
	tag string
	err string
}

func (m Model) handleModelInstalled(msg modelInstalledMsg) (Model, tea.Cmd) {
	if msg.err != "" {
		// The daemon's words distinguish a broken download from a checksum mismatch.
		m.log.Warn("completion model install failed", "tag", msg.tag, "err", msg.err)
		return m.say("the completion model was not installed: " + firstLine(msg.err)), nil
	}
	return m.say("completion model installed — your words are finished on this machine now"), nil
}
