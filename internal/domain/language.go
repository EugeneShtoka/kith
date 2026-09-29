package domain

// LanguageCandidate is a spelling dictionary the cached corpus suggests installing.
type LanguageCandidate struct {
	Tag    string // an installable dictionary
	Script string
	// Words is the weighted count behind the suggestion (own messages weigh more).
	Words int
	Share float64 // Words as a fraction of every classified word in the cache
	Bytes int64   // download size
}

// SpellSuggestion is the whole answer to "should I offer any dictionaries, and which".
type SpellSuggestion struct {
	// Why is set when no engine is installed, naming what to install.
	Why string
	// Candidates are installable, not-yet-installed dictionaries.
	Candidates []LanguageCandidate
	// Frequencies are word-frequency lists for installed dictionaries that lack one.
	Frequencies []FrequencyCandidate
}

// FrequencyCandidate is one language's word counts, offered.
type FrequencyCandidate struct {
	Tag    string
	Script string
	Share  float64 // share of your own writing in that script
	Bytes  int64   // download size
	Disk   int64   // size after pruning
	Words  int64
	// Recommended marks a dictionary that accepts far too much; Accepts is the share
	// of random three-letter strings it calls words.
	Recommended bool
	Accepts     float64
}

// Misspelling is one word worth marking in the text that was checked, and where it sits
// in it.
type Misspelling struct {
	Word        string
	Start       int
	End         int
	Suggestions []string
	// Rare: the engine accepted the word, but a much commoner one is a slip away.
	Rare bool
	// Certain: a Rare fix is safe unasked (word never seen, one commoner neighbor).
	Certain bool
}

// ModelCandidate is the local completion model, offered.
type ModelCandidate struct {
	Tag    string  // what `kith --add-model` takes
	Name   string  // the model's own name
	Bytes  int64   // download size
	Recall float64 // share of this account's next words it predicts
}

// ModelSuggestion is the whole answer to "should the local model be offered".
type ModelSuggestion struct {
	// Why is set when llama.cpp is missing, naming what to install.
	Why string
	// Candidate is the model worth offering, and Offer says whether there is one.
	Candidate ModelCandidate
	Offer     bool
}
