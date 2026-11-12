package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Offering dictionaries this account's own writing needs, detected from the cache by
// the daemon. Detecting reads every cached message, so a start asks only whether the
// daemon owes it (a new account's history has arrived), as does word that it is owed
// later; :dictionaries detects now. esc dismisses until next launch; unticking a row
// and accepting declines that language for good. Prompts chain one at a time:
// dictionaries, word frequencies, completion model.

// dictionaryOffer is the state behind the prompt.
type dictionaryOffer struct {
	// asked is set once the start's offer has been made this session.
	asked bool
	// shown is what the prompt offers, so accepting can find the unticked rows.
	shown []string
	// frequencies is the second question, held while the first is on screen.
	frequencies []domain.FrequencyCandidate
	// shownFreq is `shown` for that second prompt.
	shownFreq []string
}

// offerDictionariesCmd asks the daemon what to offer: only what it owes when owed, else
// the whole detection. requested says the person asked for it.
func (m Model) offerDictionariesCmd(owed, requested bool) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		detect := backend.DetectLanguages
		if owed {
			source, ok := backend.(api.OwedLanguages)
			if !ok {
				return dictionaryOfferMsg{} // only a daemon owes it
			}
			detect = source.OwedLanguages
		}
		found, err := detect(ctx)
		if err != nil && !requested {
			// Unprompted, so a failure is silent.
			return dictionaryOfferMsg{}
		}
		return dictionaryOfferMsg{suggestion: found, requested: requested, err: err}
	}
}

type dictionaryOfferMsg struct {
	suggestion domain.SpellSuggestion
	// requested is :dictionaries's answer, which says so when there is nothing to offer.
	requested bool
	err       error
}

// maybeOfferDictionaries makes the start's offer once, if the daemon owes one.
func (m Model) maybeOfferDictionaries() (Model, tea.Cmd) {
	if len(m.rooms.joined) == 0 {
		// Cold start with no corpus: leave `asked` unset so it is asked later.
		return m, nil
	}
	if m.offers.dictionaries.asked {
		return m, nil
	}
	m.offers.dictionaries.asked = true
	if !m.conf.base.Spell.SpellEnabled() {
		// Spelling is off; the chain continues to the completion model.
		return m.maybeOfferModel()
	}
	return m, m.offerDictionariesCmd(true, false)
}

// offerOwedDictionaries makes the offer the daemon said it owes.
func (m Model) offerOwedDictionaries() tea.Cmd {
	if !m.conf.base.Spell.SpellEnabled() {
		return nil
	}
	return m.offerDictionariesCmd(true, false)
}

// findDictionaries is :dictionaries: the offer, detected now.
func (m Model) findDictionaries() (Model, tea.Cmd) {
	if !m.conf.base.Spell.SpellEnabled() {
		return m.say("spell checking is off — see [spell] enabled"), nil
	}
	return m.say("reading what you write for its languages…"), m.offerDictionariesCmd(false, true)
}

// handleDictionaryOffer raises the chooser, or says why it cannot.
func (m Model) handleDictionaryOffer(msg dictionaryOfferMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not read what you write", msg.err), nil
	}
	if why := msg.suggestion.Why; why != "" {
		// No engine: say why once, and continue the chain.
		next, offer := m.say(firstLine(why)).maybeOfferModel()
		return next, offer
	}

	items := make([]pickerItem, 0, len(msg.suggestion.Candidates))
	// Every row starts ticked; the prompt is for opting out.
	checked := make(map[string]bool, len(msg.suggestion.Candidates))
	shown := make([]string, 0, len(msg.suggestion.Candidates))
	for _, c := range msg.suggestion.Candidates {
		if m.conf.device.Spell.SpellDeclined(c.Tag) {
			continue
		}
		items = append(items, pickerItem{
			label: c.Tag,
			value: c.Tag,
			detail: fmt.Sprintf("%s · %.0f%% of what you write · %.1f MB",
				c.Script, 100*c.Share, float64(c.Bytes)/(1<<20)),
		})
		checked[c.Tag] = true
		shown = append(shown, c.Tag)
	}
	if len(items) == 0 {
		next, offer := m.offerFrequencies(msg.suggestion.Frequencies)
		if msg.requested && next.picker.kind != pickerFrequencies {
			next = next.say("no dictionaries or word counts to offer for what you write")
		}
		return next, offer
	}
	m.offers.dictionaries.shown = shown
	m.offers.dictionaries.frequencies = msg.suggestion.Frequencies
	m.picker = newCheckedPicker(pickerDictionaries, items, checked)
	return m, nil
}

// offerFrequencies raises the word-counts prompt, second in the chain, if anything is
// left to offer.
func (m Model) offerFrequencies(candidates []domain.FrequencyCandidate) (Model, tea.Cmd) {
	items := make([]pickerItem, 0, len(candidates))
	checked := make(map[string]bool, len(candidates))
	shown := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if m.conf.device.Spell.FrequenciesDeclinedFor(c.Tag) {
			continue
		}
		items = append(items, pickerItem{
			label:  c.Tag,
			value:  c.Tag,
			detail: frequencyDetail(c),
		})
		// Only recommended rows start ticked: for some languages the download outweighs how
		// often the check would fire.
		checked[c.Tag] = c.Recommended
		shown = append(shown, c.Tag)
	}
	if len(items) == 0 {
		next, offer := m.maybeOfferModel()
		return next, offer
	}
	m.offers.dictionaries.shownFreq = shown
	m.picker = newCheckedPicker(pickerFrequencies, items, checked)
	return m, nil
}

// frequencyDetail is the evidence on a row: share, acceptance rate, and size.
func frequencyDetail(c domain.FrequencyCandidate) string {
	out := fmt.Sprintf("%s · %.0f%% of what you write", c.Script, 100*c.Share)
	if c.Accepts > 0 {
		out += fmt.Sprintf(" · dictionary accepts %.0f%% of 3-letter strings", 100*c.Accepts)
	}
	return out + fmt.Sprintf(" · %.1f MB → %.1f MB kept", float64(c.Bytes)/(1<<20), float64(c.Disk)/(1<<20))
}

// acceptFrequencyOffer installs what was ticked and records what was not.
func (m Model) acceptFrequencyOffer(tags []string) (Model, tea.Cmd) {
	declined := unticked(m.offers.dictionaries.shownFreq, tags)
	m = m.closePicker()
	next, save := m.declineFrequencies(declined)
	install, fetch := next.installFrequencies(tags)
	answered, offer := install.maybeOfferModel()
	return answered, tea.Batch(save, fetch, offer)
}

// declineFrequencies records the refusals in the config, separately from dictionaries.
func (m Model) declineFrequencies(tags []string) (Model, tea.Cmd) {
	if len(tags) == 0 {
		return m, nil
	}
	dev := m.conf.device.Clone()
	dev.Spell.FrequenciesDeclined = append(
		append([]string(nil), dev.Spell.FrequenciesDeclined...), tags...)
	return m.applyDevice(dev, "")
}

// installFrequencies fetches the ticked lists off the event loop, reporting once.
func (m Model) installFrequencies(tags []string) (Model, tea.Cmd) {
	if len(tags) == 0 {
		return m, nil
	}
	return m.installEach("word counts for", tags, m.backend.InstallFrequencies, func(done, failed []string) tea.Msg {
		return frequenciesInstalledMsg{installed: done, failed: failed}
	})
}

type frequenciesInstalledMsg struct {
	installed []string
	failed    []string
}

func (m Model) handleFrequenciesInstalled(msg frequenciesInstalledMsg) (Model, tea.Cmd) {
	switch {
	case len(msg.failed) > 0 && len(msg.installed) > 0:
		return m.say(fmt.Sprintf("word counts installed for %s; %s could not be verified",
			strings.Join(msg.installed, ", "), strings.Join(msg.failed, ", "))), nil
	case len(msg.failed) > 0:
		// A checksum mismatch is a refusal, not a retry.
		return m.say("word counts for " + strings.Join(msg.failed, ", ") +
			" could not be verified and were not installed"), nil
	default:
		return m.say("word counts installed for " + strings.Join(msg.installed, ", ") +
			" — rare words are now checked"), nil
	}
}

// acceptDictionaryOffer installs what was ticked and records what was not, then
// raises the frequencies prompt.
func (m Model) acceptDictionaryOffer(tags []string) (Model, tea.Cmd) {
	declined := unticked(m.offers.dictionaries.shown, tags)
	m = m.closePicker()
	next, save := m.declineDictionaries(declined)
	install, fetch := next.installDictionaries(tags)
	// With no frequencies to offer this moves on to the model offer, whose Cmd must run.
	install, offer := install.offerFrequencies(install.offers.dictionaries.frequencies)
	return install, tea.Batch(save, fetch, offer)
}

// declineDictionaries writes the refusals to the config so they outlive the session.
func (m Model) declineDictionaries(tags []string) (Model, tea.Cmd) {
	if len(tags) == 0 {
		return m, nil
	}
	dev := m.conf.device.Clone()
	dev.Spell.Declined = append(append([]string(nil), dev.Spell.Declined...), tags...)
	return m.applyDevice(dev, "")
}

// installDictionaries fetches the ticked ones off the event loop, reporting once.
func (m Model) installDictionaries(tags []string) (Model, tea.Cmd) {
	if len(tags) == 0 {
		return m, nil
	}
	return m.installEach("", tags, m.backend.InstallDictionary, func(done, failed []string) tea.Msg {
		return dictionariesInstalledMsg{installed: done, failed: failed}
	})
}

type dictionariesInstalledMsg struct {
	installed []string
	failed    []string
}

func (m Model) handleDictionariesInstalled(msg dictionariesInstalledMsg) (Model, tea.Cmd) {
	switch {
	case len(msg.failed) > 0 && len(msg.installed) > 0:
		return m.say(fmt.Sprintf("installed %s; %s could not be verified and were not installed",
			strings.Join(msg.installed, ", "), strings.Join(msg.failed, ", "))), nil
	case len(msg.failed) > 0:
		// A checksum mismatch is a refusal, not a retry.
		return m.say(strings.Join(msg.failed, ", ") + " could not be verified and were not installed"), nil
	default:
		return m.say("installed " + strings.Join(msg.installed, ", ") + " — spellcheck is ready"), nil
	}
}

// unticked is the rows shown but not chosen.
func unticked(shown, chosen []string) []string {
	var out []string
	for _, tag := range shown {
		if !slices.Contains(chosen, tag) {
			out = append(out, tag)
		}
	}
	return out
}

// installEach says it is fetching what for tags, then installs each off the event loop
// and reports which were installed and which failed.
func (m Model) installEach(
	what string, tags []string, install func(context.Context, string) error, report func(done, failed []string) tea.Msg,
) (Model, tea.Cmd) {
	m = m.say(strings.TrimSpace("fetching "+what) + " " + strings.Join(tags, ", ") + "…")
	ctx := m.ctx
	return m, func() tea.Msg {
		var done, failed []string
		for _, tag := range tags {
			if install(ctx, tag) != nil {
				failed = append(failed, tag)
			} else {
				done = append(done, tag)
			}
		}
		return report(done, failed)
	}
}
