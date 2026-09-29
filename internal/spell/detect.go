package spell

import (
	"sort"
	"strings"
)

// Language detection counts words (not letters) per script in the cached corpus and
// offers the languages found. Own messages weigh more than read ones. Marker sets must
// not contain look-alike ASCII (an ASCII "I" once made all English Ukrainian).
const ownWeight = 4

// A language is offered when it clears both a share and an absolute floor.
const (
	minShare = 0.01
	minWords = 50
)

// Candidate is a language the corpus says is worth a dictionary.
type Candidate struct {
	// Tag is always one the manifest carries.
	Tag    string
	Script Script
	// Words is the weighted count and Share its fraction of everything classified.
	Words int
	Share float64
}

// Counts is a corpus being measured. The zero value is ready.
type Counts struct {
	byScript map[Script]int
	// markers counts words with letters unique to one language within a script.
	markers map[string]int
	total   int
}

// Add folds one message in; mine says whether the account wrote it.
func (c *Counts) Add(text string, mine bool) {
	if c.byScript == nil {
		c.byScript = map[Script]int{}
		c.markers = map[string]int{}
	}
	weight := 1
	if mine {
		weight = ownWeight
	}
	for _, w := range Words(text) {
		if w.Script == Other {
			continue
		}
		c.byScript[w.Script] += weight
		c.total += weight
		for lang, set := range distinctive {
			if strings.ContainsAny(w.Text, set) {
				c.markers[lang] += weight
			}
		}
	}
}

// distinctive maps a tag to letters used in that language but not its script
// neighbors. English is the Latin default, not a match.
var distinctive = map[string]string{
	"ru_RU": "ыЫъЪэЭ",
	// Cyrillic і (U+0456) and І (U+0406), not Latin i and I.
	"uk_UA": "іІїЇєЄґҐ",
	"de_DE": "äöüßÄÖÜ",
	"fr_FR": "éèêçàùûôÉÈÇÀ",
	// Letters only: Words() trims punctuation like ¿.
	"es_ES": "ñáíóúÑ",
	"pl_PL": "ąćęłńśźżĄĆĘŁŃŚŹŻ",
	"pt_BR": "ãõçáêóÃÕÇ",
	"it_IT": "àèìòùÀÈÌÒÙ",
	// No Dutch: it has no letter English lacks. nl_NL is installable by name.
}

// markerScript is the script each marker language is written in.
var markerScript = map[string]Script{
	"ru_RU": Cyrillic, "uk_UA": Cyrillic,
	"de_DE": Latin, "fr_FR": Latin, "es_ES": Latin,
	"pl_PL": Latin, "pt_BR": Latin, "it_IT": Latin,
}

// Candidates are the languages worth offering, most words first.
func (c Counts) Candidates() []Candidate {
	if c.total == 0 {
		return nil
	}
	var out []Candidate
	for script, words := range c.byScript {
		share := float64(words) / float64(c.total)
		if words < minWords || share < minShare {
			continue
		}
		for _, tag := range c.tagsFor(script) {
			if _, ok := dictionarySources[tag]; !ok {
				continue
			}
			out = append(out, Candidate{Tag: tag, Script: script, Words: words, Share: share})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Words != out[j].Words {
			return out[i].Words > out[j].Words
		}
		return out[i].Tag < out[j].Tag
	})
	return out
}

// tagsFor turns a script into the dictionaries to offer: Cyrillic picks by marker
// letters; Latin offers English plus every accented language clearing markerShare.
func (c Counts) tagsFor(script Script) []string {
	switch script {
	case Hebrew:
		return []string{"he_IL"}
	case Cyrillic:
		return []string{c.pick("ru_RU", "uk_UA")}
	case Latin:
		tags := []string{"en_US"}
		for tag, s := range markerScript {
			if s == Latin && c.markerShare(tag, Latin) >= markerShare {
				tags = append(tags, tag)
			}
		}
		sort.Strings(tags)
		return tags
	case Arabic, Greek, Other:
		return nil
	}
	return nil
}

// markerShare is the fraction of a script's words carrying tag's marker letters.
func (c Counts) markerShare(tag string, script Script) float64 {
	words := c.byScript[script]
	if words == 0 {
		return 0
	}
	return float64(c.markers[tag]) / float64(words)
}

// markerShare is the bar for an accented language; low because accents are sparse and
// an extra dictionary is cheap.
const markerShare = 0.02

// pick chooses whichever language has more marker words, defaulting to first.
func (c Counts) pick(first, second string) string {
	if c.markers[second] > c.markers[first] {
		return second
	}
	return first
}

// languageNames are the human names of the marker languages.
var languageNames = map[string]string{
	"ru_RU": "Russian", "uk_UA": "Ukrainian",
	"de_DE": "German", "fr_FR": "French", "es_ES": "Spanish",
	"pl_PL": "Polish", "pt_BR": "Portuguese", "it_IT": "Italian",
}

// scriptLanguage is the language assumed for a script with no marker letters.
var scriptLanguage = map[Script]string{
	Latin:    "English",
	Cyrillic: "Russian",
	Hebrew:   "Hebrew",
	Arabic:   "Arabic",
	Greek:    "Greek",
}

// LanguageOf names the language text is written in, or "" when it holds no words:
// majority script, then marker letters, then the script's default. Shares the
// detector's machinery so the two never disagree.
func LanguageOf(text string) string {
	words := Words(text)
	if len(words) == 0 {
		return ""
	}
	byScript := map[Script]int{}
	markers := map[string]int{}
	for _, word := range words {
		byScript[word.Script]++
		for tag, letters := range distinctive {
			if markerScript[tag] != word.Script {
				continue
			}
			if strings.ContainsAny(word.Text, letters) {
				markers[tag]++
			}
		}
	}
	best, bestWords := Other, 0
	for script, n := range byScript {
		if n > bestWords {
			best, bestWords = script, n
		}
	}
	leader, leaderCount := "", 0
	for tag, n := range markers {
		if markerScript[tag] == best && n > leaderCount {
			leader, leaderCount = tag, n
		}
	}
	if name, ok := languageNames[leader]; ok && leaderCount > 0 {
		return name
	}
	return scriptLanguage[best]
}
