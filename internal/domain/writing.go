package domain

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A message's language is read from its letters, never guessed from statistics: the
// writing system is exact from one character, and a letter only some languages use
// (ы, ß, ơ) narrows the languages it can be. What the letters cannot settle stays a
// list of suspects, so a rule never acts on a guess. Measured 2026-10-13 against
// 21,838 cached messages: every statistical identifier tried (whatlanggo, lingua-go,
// langid-go) read short Russian as Bulgarian and short English as Afrikaans with
// full confidence, and langid-go read "Да" as Chinese.

// Language is what a message's letters say about the language it is written in. The
// zero value is a message with no letters (emoji, a bare link, digits).
type Language struct {
	// Script is the writing system most of its words are in, as writingSystems names it.
	Script string
	// Suspects are the language codes it may be in, the likeliest first.
	Suspects []string
	// Likely is the language the letters settle on: the only suspect, or the one a
	// script's readers almost always mean (Chinese for Chinese characters); else "".
	Likely string
}

// Name is the language in English words, for a prompt: the likely one, else the first
// suspect (English for Latin letters alone); "" with no letters.
func (l Language) Name() string {
	code := l.Likely
	if code == "" && len(l.Suspects) > 0 {
		code = l.Suspects[0]
	}
	return languageNames[code]
}

// In reports whether the message is in one of codes: the likely language is, or every
// suspect is. A message the letters cannot narrow is in no particular language.
func (l Language) In(codes []string) bool {
	if len(l.Suspects) == 0 {
		return false
	}
	if l.Likely != "" && slices.Contains(codes, l.Likely) {
		return true
	}
	for _, s := range l.Suspects {
		if !slices.Contains(codes, s) {
			return false
		}
	}
	return true
}

// Outside reports whether the message cannot be in any of codes: it has letters and no
// suspect is among them. Plain Latin letters could be English, so they are never
// outside a list naming it.
func (l Language) Outside(codes []string) bool {
	if len(l.Suspects) == 0 {
		return false
	}
	return !slices.ContainsFunc(l.Suspects, func(s string) bool { return slices.Contains(codes, s) })
}

// writingSystem is one script: its Unicode tables, whether it is written without
// spaces (each character then counts as a word), and the languages written in it, the
// likeliest first. likely is set when the script alone settles the language.
type writingSystem struct {
	name      string
	tables    []*unicode.RangeTable
	spaceless bool
	languages []string
	likely    bool
}

// Kana is Japanese writing: hiragana or katakana, with whatever kanji is beside them.
// Han is Chinese characters with no kana at all.
const (
	scriptKana   = "Kana"
	scriptHan    = "Han"
	scriptHangul = "Hangul"
)

// writingSystems is every script told apart, the commonest first (the order a letter
// is looked up in).
var writingSystems = []writingSystem{
	{name: "Latin", tables: []*unicode.RangeTable{unicode.Latin}, languages: []string{
		"en", "de", "fr", "es", "it", "pt", "nl", "pl", "cs", "sk", "sl", "hr", "bs", "sr", "ro", "hu",
		"tr", "az", "sv", "da", "no", "is", "fi", "et", "lv", "lt", "sq", "ca", "gl", "eu", "ga",
		"cy", "mt", "vi", "id", "ms", "tl", "sw", "af", "uz", "tk",
	}},
	{name: "Cyrillic", tables: []*unicode.RangeTable{unicode.Cyrillic}, languages: []string{
		"ru", "uk", "be", "bg", "mk", "sr", "kk", "ky", "mn", "tg",
	}},
	{name: "Hebrew", tables: []*unicode.RangeTable{unicode.Hebrew}, languages: []string{"he", "yi"}, likely: true},
	{name: "Arabic", tables: []*unicode.RangeTable{unicode.Arabic}, languages: []string{"ar", "fa", "ur", "ps", "ku"}},
	{name: "Greek", tables: []*unicode.RangeTable{unicode.Greek}, languages: []string{"el"}},
	{name: scriptHan, tables: []*unicode.RangeTable{unicode.Han}, spaceless: true, languages: []string{"zh", "ja"}, likely: true},
	{name: scriptKana, tables: []*unicode.RangeTable{unicode.Hiragana, unicode.Katakana}, spaceless: true, languages: []string{"ja"}},
	{name: scriptHangul, tables: []*unicode.RangeTable{unicode.Hangul}, languages: []string{"ko"}},
	{name: "Thai", tables: []*unicode.RangeTable{unicode.Thai}, spaceless: true, languages: []string{"th"}},
	{name: "Lao", tables: []*unicode.RangeTable{unicode.Lao}, spaceless: true, languages: []string{"lo"}},
	{name: "Khmer", tables: []*unicode.RangeTable{unicode.Khmer}, spaceless: true, languages: []string{"km"}},
	{name: "Myanmar", tables: []*unicode.RangeTable{unicode.Myanmar}, spaceless: true, languages: []string{"my"}},
	{name: "Devanagari", tables: []*unicode.RangeTable{unicode.Devanagari}, languages: []string{"hi", "mr", "ne"}},
	{name: "Bengali", tables: []*unicode.RangeTable{unicode.Bengali}, languages: []string{"bn"}},
	{name: "Gurmukhi", tables: []*unicode.RangeTable{unicode.Gurmukhi}, languages: []string{"pa"}},
	{name: "Gujarati", tables: []*unicode.RangeTable{unicode.Gujarati}, languages: []string{"gu"}},
	{name: "Tamil", tables: []*unicode.RangeTable{unicode.Tamil}, languages: []string{"ta"}},
	{name: "Telugu", tables: []*unicode.RangeTable{unicode.Telugu}, languages: []string{"te"}},
	{name: "Kannada", tables: []*unicode.RangeTable{unicode.Kannada}, languages: []string{"kn"}},
	{name: "Malayalam", tables: []*unicode.RangeTable{unicode.Malayalam}, languages: []string{"ml"}},
	{name: "Sinhala", tables: []*unicode.RangeTable{unicode.Sinhala}, languages: []string{"si"}},
	{name: "Armenian", tables: []*unicode.RangeTable{unicode.Armenian}, languages: []string{"hy"}},
	{name: "Georgian", tables: []*unicode.RangeTable{unicode.Georgian}, languages: []string{"ka"}},
	{name: "Ethiopic", tables: []*unicode.RangeTable{unicode.Ethiopic}, languages: []string{"am", "ti"}},
}

// telltales are letters only some of a script's languages use, lower case. Letters
// most of them share (é, à) tell nothing and are left out.
var telltales = []struct {
	letters   string
	languages []string
}{
	{"ß", []string{"de"}},
	{"ä", []string{"de", "sv", "fi", "et", "sk"}},
	{"ö", []string{"de", "sv", "fi", "et", "tr", "hu", "is", "az"}},
	{"ü", []string{"de", "tr", "et", "hu", "az"}},
	{"ñ", []string{"es", "tl"}},
	{"ç", []string{"fr", "pt", "tr", "ca", "az", "sq"}},
	{"ãõ", []string{"pt", "et"}},
	{"ł", []string{"pl"}},
	{"śźżćń", []string{"pl"}},
	{"ąę", []string{"pl", "lt"}},
	{"řůě", []string{"cs"}},
	{"őű", []string{"hu"}},
	{"ğış", []string{"tr", "az"}},
	{"șțâî", []string{"ro"}},
	{"ă", []string{"ro", "vi"}},
	{"đ", []string{"vi", "hr", "bs", "sr"}},
	{"ơư", []string{"vi"}},
	{"å", []string{"sv", "da", "no", "fi"}},
	{"æø", []string{"da", "no"}},
	{"þð", []string{"is"}},
	{"ыэё", []string{"ru", "be"}},
	{"ъ", []string{"ru", "bg"}},
	{"і", []string{"uk", "be", "kk"}},
	{"їєґ", []string{"uk"}},
	{"ў", []string{"be"}},
	{"ђћ", []string{"sr"}},
	{"јљњџ", []string{"sr", "mk"}},
	{"ѓќѕ", []string{"mk"}},
	{"әғқңұһ", []string{"kk"}},
	{"үө", []string{"kk", "ky", "mn"}},
	{"پچژگ", []string{"fa", "ur", "ku", "ps"}},
	{"ٹڈڑںے", []string{"ur"}},
}

// vietnamese is the precomposed Latin block of stacked tone marks (ạ, ấ, ữ), which
// only Vietnamese writes.
func vietnamese(r rune) bool { return r >= 0x1EA0 && r <= 0x1EF9 }

// LanguageOf reads body's letters. Links, Matrix IDs, :shortcodes:, code and faces
// (kaomoji) are not words anyone wrote in a language and are skipped.
//
// Telltales rule a language out only when half the words of their script carry one it
// never uses: "I live in Zürich" is still English, "Xin chào, bạn khỏe" is Vietnamese.
// A message as much in a second script as in its first ("Нет façade") is mixed, and
// keeps the suspects of both.
func LanguageOf(body string) Language {
	tally := make([]scriptTally, len(writingSystems))
	for _, word := range languageWords(body) {
		for _, run := range letterRuns(word) {
			countRun(tally, run)
		}
	}
	units := make([]int, len(tally))
	for i := range tally {
		units[i] = tally[i].words
	}
	top := mostWritten(units)
	if top < 0 {
		return Language{}
	}
	out := Language{Script: writingSystems[top].name, Suspects: tally[top].suspects(writingSystems[top].languages)}
	mixed := false
	for i, n := range units {
		if i != top && n > 0 && n*2 >= units[top] {
			mixed = true
			for _, s := range tally[i].suspects(writingSystems[i].languages) {
				if !slices.Contains(out.Suspects, s) {
					out.Suspects = append(out.Suspects, s)
				}
			}
		}
	}
	if !mixed && (len(out.Suspects) == 1 || writingSystems[top].likely) {
		out.Likely = out.Suspects[0]
	}
	return out
}

// scriptTally is what one script's words say: how many there are, and the telltale
// sets each carried.
type scriptTally struct {
	words int
	marks [][][]string // per word that carried any, its telltale sets
}

// countRun adds one run of letters to the tally: a word per stretch of one script, or a
// character per character of a script written without spaces.
func countRun(tally []scriptTally, run string) {
	last := -1
	var carried [][]string
	for _, r := range run {
		at := writingSystemOf(r)
		if at < 0 {
			continue
		}
		if writingSystems[at].spaceless || at != last {
			tally[at].words++
			if last >= 0 && len(carried) > 0 {
				tally[last].marks = append(tally[last].marks, carried)
			}
			carried = nil
		}
		last = at
		if set := telltale(unicode.ToLower(r)); set != nil &&
			!slices.ContainsFunc(carried, func(c []string) bool { return slices.Equal(c, set) }) {
			carried = append(carried, set)
		}
	}
	if last >= 0 && len(carried) > 0 {
		tally[last].marks = append(tally[last].marks, carried)
	}
}

// suspects are the script's languages its telltales leave: one is ruled out when half
// the script's words carry a letter it never uses. Telltales that rule out every
// language contradict each other and decide nothing.
func (t scriptTally) suspects(languages []string) []string {
	out := slices.DeleteFunc(slices.Clone(languages), func(lang string) bool {
		against := 0
		for _, sets := range t.marks {
			if slices.ContainsFunc(sets, func(set []string) bool { return !slices.Contains(set, lang) }) {
				against++
			}
		}
		return against*2 >= t.words
	})
	if len(out) == 0 {
		return slices.Clone(languages)
	}
	return out
}

// mostWritten is the writing system with the most words, -1 for none. Kanji beside
// kana is Japanese and hanja beside hangul is Korean, so Chinese characters count
// toward either when it is present.
func mostWritten(units []int) int {
	han := indexOfScript(scriptHan)
	for _, host := range []string{scriptKana, scriptHangul} {
		if at := indexOfScript(host); units[at] > 0 {
			units[at] += units[han]
			units[han] = 0
		}
	}
	best := -1
	for i, n := range units {
		if n > 0 && (best < 0 || n > units[best]) {
			best = i
		}
	}
	return best
}

func indexOfScript(name string) int {
	return slices.IndexFunc(writingSystems, func(ws writingSystem) bool { return ws.name == name })
}

// writingSystemOf is the index of the script letter r is in, -1 for anything else.
func writingSystemOf(r rune) int {
	if !unicode.IsLetter(r) {
		return -1
	}
	for i := range writingSystems {
		if unicode.IsOneOf(writingSystems[i].tables, r) {
			return i
		}
	}
	return -1
}

// telltale is the languages a lower-case letter narrows to, nil when it narrows nothing.
func telltale(r rune) []string {
	if vietnamese(r) {
		return []string{"vi"}
	}
	for _, t := range telltales {
		if strings.ContainsRune(t.letters, r) {
			return t.languages
		}
	}
	return nil
}

// languageWords is body split on space, less code spans and the tokens nobody wrote in
// a language (notALanguageWord).
func languageWords(body string) []string {
	var prose strings.Builder
	for i, part := range strings.Split(body, "`") {
		if i%2 == 0 {
			prose.WriteString(part)
			prose.WriteByte(' ')
		}
	}
	return slices.DeleteFunc(strings.Fields(prose.String()), notALanguageWord)
}

// notALanguageWord reports a link, a Matrix ID or alias, or a :shortcode:.
func notALanguageWord(token string) bool {
	lower := strings.ToLower(token)
	switch {
	case strings.Contains(lower, "://"), strings.HasPrefix(lower, "www."):
		return true
	case strings.HasPrefix(token, "@"), strings.HasPrefix(token, "#"), strings.HasPrefix(token, "!"):
		return strings.Contains(token, ":")
	}
	// A shortcode may have punctuation or an emoji after it: great :+1:!
	code := strings.TrimRightFunc(token, func(r rune) bool { return r != ':' && (unicode.IsPunct(r) || unicode.IsSymbol(r)) })
	return len(code) > 2 && strings.HasPrefix(code, ":") && strings.HasSuffix(code, ":")
}

// letterRuns are a token's runs of letters that can be words. In a face every letter
// stands alone between symbols, (ノ°Д°)ノ or ¯\_(ツ)_/¯, so a lone letter counts only
// as the whole token (я, в), and never a lone kana, which is the face's eyes or mouth.
// Punctuation, digits and emoji beside or inside a word leave it a word: 你好，朋友😀.
func letterRuns(token string) []string {
	word := strings.TrimFunc(token, func(r rune) bool { return !unicode.IsLetter(r) })
	if r, size := utf8.DecodeRuneInString(word); size == len(word) {
		if size == 0 || unicode.IsOneOf(writingSystems[indexOfScript(scriptKana)].tables, r) {
			return nil
		}
		return []string{word}
	}
	var runs []string
	for run := range strings.FieldsFuncSeq(word, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsMark(r) }) {
		if utf8.RuneCountInString(run) > 1 {
			runs = append(runs, run)
		}
	}
	return runs
}

// A rule's `lang` and `not_lang` entries are language codes (ISO 639-1), or a named
// set: `family:` a language family, `region:` the languages of a UN region.
const (
	familyPrefix = "family:"
	regionPrefix = "region:"
)

// languageFamilies are the families a rule may name. A family is linguistic, so
// Chinese and Japanese share none: name a region for languages a place has in common.
var languageFamilies = map[string][]string{
	"slavic":        {"ru", "uk", "be", "bg", "mk", "sr", "hr", "bs", "sl", "cs", "sk", "pl"},
	"germanic":      {"en", "de", "nl", "sv", "da", "no", "is", "af", "yi"},
	"romance":       {"fr", "es", "it", "pt", "ro", "ca", "gl"},
	"baltic":        {"lv", "lt"},
	"celtic":        {"ga", "cy"},
	"uralic":        {"fi", "et", "hu"},
	"turkic":        {"tr", "az", "kk", "ky", "uz", "tk"},
	"semitic":       {"ar", "he", "mt", "am", "ti"},
	"indo-iranian":  {"hi", "mr", "ne", "bn", "pa", "gu", "si", "fa", "ur", "ps", "ku", "tg"},
	"dravidian":     {"ta", "te", "kn", "ml"},
	"sino-tibetan":  {"zh", "my"},
	"japonic":       {"ja"},
	"koreanic":      {"ko"},
	"kra-dai":       {"th", "lo"},
	"austroasiatic": {"vi", "km"},
	"austronesian":  {"id", "ms", "tl"},
	"mongolic":      {"mn"},
	"hellenic":      {"el"},
	"armenian":      {"hy"},
	"kartvelian":    {"ka"},
	"albanian":      {"sq"},
	"basque":        {"eu"},
	"bantu":         {"sw"},
}

// languageRegions are UN M49 regions, each language placed by its main country.
var languageRegions = map[string][]string{
	"asia-east":      {"zh", "ja", "ko", "mn"},
	"asia-southeast": {"vi", "th", "lo", "km", "my", "id", "ms", "tl"},
	"asia-south":     {"hi", "mr", "ne", "bn", "pa", "gu", "ta", "te", "kn", "ml", "si", "ur", "ps", "fa"},
	"asia-central":   {"kk", "ky", "uz", "tk", "tg"},
	"asia-west":      {"ar", "he", "tr", "az", "hy", "ka", "ku"},
	"africa-north":   {"ar"},
	"africa-east":    {"am", "ti", "sw"},
	"europe-east":    {"ru", "uk", "be", "bg", "pl", "cs", "sk", "hu", "ro"},
	"europe-north":   {"en", "ga", "cy", "sv", "da", "no", "is", "fi", "et", "lv", "lt"},
	"europe-south":   {"it", "es", "pt", "ca", "gl", "eu", "el", "mt", "sq", "sr", "hr", "bs", "sl", "mk"},
	"europe-west":    {"de", "fr", "nl"},
}

// languageNames is every language code a rule may name, in English words.
var languageNames = map[string]string{
	"af": "Afrikaans", "am": "Amharic", "ar": "Arabic", "az": "Azerbaijani", "be": "Belarusian",
	"bg": "Bulgarian", "bn": "Bengali", "bs": "Bosnian", "ca": "Catalan", "cs": "Czech", "cy": "Welsh",
	"da": "Danish", "de": "German", "el": "Greek", "en": "English", "es": "Spanish", "et": "Estonian",
	"eu": "Basque", "fa": "Persian", "fi": "Finnish", "fr": "French", "ga": "Irish", "gl": "Galician",
	"gu": "Gujarati", "he": "Hebrew", "hi": "Hindi", "hr": "Croatian", "hu": "Hungarian", "hy": "Armenian",
	"id": "Indonesian", "is": "Icelandic", "it": "Italian", "ja": "Japanese", "ka": "Georgian",
	"kk": "Kazakh", "km": "Khmer", "kn": "Kannada", "ko": "Korean", "ku": "Kurdish", "ky": "Kyrgyz",
	"lo": "Lao", "lt": "Lithuanian", "lv": "Latvian", "mk": "Macedonian", "ml": "Malayalam",
	"mn": "Mongolian", "mr": "Marathi", "ms": "Malay", "mt": "Maltese", "my": "Burmese", "ne": "Nepali",
	"nl": "Dutch", "no": "Norwegian", "pa": "Punjabi", "pl": "Polish", "ps": "Pashto", "pt": "Portuguese",
	"ro": "Romanian", "ru": "Russian", "si": "Sinhala", "sk": "Slovak", "sl": "Slovenian", "sq": "Albanian",
	"sr": "Serbian", "sv": "Swedish", "sw": "Swahili", "ta": "Tamil", "te": "Telugu", "tg": "Tajik",
	"th": "Thai", "ti": "Tigrinya", "tk": "Turkmen", "tl": "Tagalog", "tr": "Turkish", "uk": "Ukrainian",
	"ur": "Urdu", "uz": "Uzbek", "vi": "Vietnamese", "yi": "Yiddish", "zh": "Chinese",
}

// LanguageCodes expands a rule's `lang` or `not_lang` entries into language codes,
// refusing an entry it does not know: a mistyped code read as "nothing" would turn a
// `not_lang` rule against every message.
func LanguageCodes(entries []string) ([]string, error) {
	var out []string
	for _, entry := range entries {
		key := strings.ToLower(strings.TrimSpace(entry))
		var codes []string
		switch {
		case strings.HasPrefix(key, familyPrefix):
			codes = languageFamilies[strings.TrimPrefix(key, familyPrefix)]
		case strings.HasPrefix(key, regionPrefix):
			codes = languageRegions[strings.TrimPrefix(key, regionPrefix)]
		case languageNames[key] != "":
			codes = []string{key}
		}
		if len(codes) == 0 {
			return nil, fmt.Errorf("%q is not a language code (en, zh), a family:… or a region:…", entry)
		}
		for _, c := range codes {
			if !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

// ScriptNamed is the writing system a rule's `script` entry names, ignoring case.
func ScriptNamed(entry string) (string, error) {
	key := strings.TrimSpace(entry)
	for _, ws := range writingSystems {
		if strings.EqualFold(ws.name, key) {
			return ws.name, nil
		}
	}
	names := make([]string, len(writingSystems))
	for i, ws := range writingSystems {
		names[i] = ws.name
	}
	return "", fmt.Errorf("%q is not a writing system (%s)", entry, strings.Join(names, ", "))
}
