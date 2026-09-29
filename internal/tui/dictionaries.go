package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Offering the dictionaries this account's own writing asks for, detected from the
// cache by the daemon. Never automatic. esc dismisses (asked again next launch);
// unticking a row and accepting declines that language for good. The prompts are a
// chain — dictionaries, word frequencies, completion model — one at a time.

// dictionaryOffer is the state behind the one-time prompt.
type dictionaryOffer struct {
	// asked is set once the offer has been made this session.
	asked bool
	// shown is what the prompt offers, so accepting can find the unticked rows.
	shown []string
	// frequencies is the second question, held while the first is on screen.
	frequencies []domain.FrequencyCandidate
	// shownFreq is `shown` for that second prompt.
	shownFreq []string
}

// offerDictionariesCmd asks the daemon what languages the corpus holds.
func (m Model) offerDictionariesCmd() tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		found, err := backend.DetectLanguages(ctx)
		if err != nil {
			// Unprompted, so a failure is silent.
			return dictionaryOfferMsg{}
		}
		return dictionaryOfferMsg{suggestion: found}
	}
}

// dictionaryOfferMsg carries the detector's answer back to the event loop.
type dictionaryOfferMsg struct {
	suggestion domain.SpellSuggestion
}

// maybeOfferDictionaries decides whether to ask at all, and asks once.
func (m Model) maybeOfferDictionaries() (Model, tea.Cmd) {
	if len(m.rooms.joined) == 0 {
		// Cold start with no corpus yet: do not set `asked`, so it is asked later.
		return m, nil
	}
	if m.offers.dictionaries.asked {
		return m, nil
	}
	m.offers.dictionaries.asked = true
	if !m.conf.base.Spell.SpellEnabled() {
		// Spelling is off, but the chain continues to the completion model.
		return m.maybeOfferModel()
	}
	return m, m.offerDictionariesCmd()
}

// handleDictionaryOffer raises the chooser, or says why it cannot.
func (m Model) handleDictionaryOffer(msg dictionaryOfferMsg) (Model, tea.Cmd) {
	if why := msg.suggestion.Why; why != "" {
		// No engine: say why once, and continue the chain.
		next, offer := m.say(firstLine(why)).maybeOfferModel()
		return next, offer
	}

	items := make([]pickerItem, 0, len(msg.suggestion.Candidates))
	// Every row starts ticked; the prompt exists to let somebody disagree.
	checked := make(map[string]bool, len(msg.suggestion.Candidates))
	shown := make([]string, 0, len(msg.suggestion.Candidates))
	for _, c := range msg.suggestion.Candidates {
		if m.conf.base.Spell.SpellDeclined(c.Tag) {
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
		return m.offerFrequencies(msg.suggestion.Frequencies)
	}
	m.offers.dictionaries.shown = shown
	m.offers.dictionaries.frequencies = msg.suggestion.Frequencies
	m.picker = newCheckedPicker(pickerDictionaries, items, checked)
	return m, nil
}

// offerFrequencies raises the word-counts prompt, second in the chain, if there is
// anything left to offer.
func (m Model) offerFrequencies(candidates []domain.FrequencyCandidate) (Model, tea.Cmd) {
	items := make([]pickerItem, 0, len(candidates))
	checked := make(map[string]bool, len(candidates))
	shown := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if m.conf.base.Spell.FrequenciesDeclinedFor(c.Tag) {
			continue
		}
		items = append(items, pickerItem{
			label:  c.Tag,
			value:  c.Tag,
			detail: frequencyDetail(c),
		})
		// Only recommended rows start ticked: for some languages the download far
		// outweighs how often the check would fire.
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
	cfg := m.conf.base.Clone()
	cfg.Spell.FrequenciesDeclined = append(
		append([]string(nil), cfg.Spell.FrequenciesDeclined...), tags...)
	return m.applyConfig(cfg, "", "")
}

// installFrequencies fetches the ticked lists off the event loop, reporting once.
func (m Model) installFrequencies(tags []string) (Model, tea.Cmd) {
	if len(tags) == 0 {
		return m, nil
	}
	m = m.say("fetching word counts for " + strings.Join(tags, ", ") + "…")
	backend, ctx := m.backend, m.ctx
	return m, func() tea.Msg {
		done, failed := installEach(tags, func(tag string) error { return backend.InstallFrequencies(ctx, tag) })
		return frequenciesInstalledMsg{installed: done, failed: failed}
	}
}

// frequenciesInstalledMsg is what came of it.
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
	// With no frequencies to offer this goes on to the model offer, whose Cmd must run.
	install, offer := install.offerFrequencies(install.offers.dictionaries.frequencies)
	return install, tea.Batch(save, fetch, offer)
}

// declineDictionaries writes the refusals to the config, so they outlive the session.
func (m Model) declineDictionaries(tags []string) (Model, tea.Cmd) {
	if len(tags) == 0 {
		return m, nil
	}
	cfg := m.conf.base.Clone()
	cfg.Spell.Declined = append(append([]string(nil), cfg.Spell.Declined...), tags...)
	return m.applyConfig(cfg, "", "")
}

// installDictionaries fetches the ticked ones off the event loop, reporting once.
func (m Model) installDictionaries(tags []string) (Model, tea.Cmd) {
	if len(tags) == 0 {
		return m, nil
	}
	m = m.say("fetching " + strings.Join(tags, ", ") + "…")
	backend, ctx := m.backend, m.ctx
	return m, func() tea.Msg {
		done, failed := installEach(tags, func(tag string) error { return backend.InstallDictionary(ctx, tag) })
		return dictionariesInstalledMsg{installed: done, failed: failed}
	}
}

// dictionariesInstalledMsg is what came of it.
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

// unticked is the rows shown but not chosen: the declined ones.
func unticked(shown, chosen []string) []string {
	var out []string
	for _, tag := range shown {
		if !slices.Contains(chosen, tag) {
			out = append(out, tag)
		}
	}
	return out
}

// installEach runs install for every tag, splitting them by outcome.
func installEach(tags []string, install func(string) error) (done, failed []string) {
	for _, tag := range tags {
		if install(tag) != nil {
			failed = append(failed, tag)
		} else {
			done = append(done, tag)
		}
	}
	return done, failed
}
