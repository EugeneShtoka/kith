package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Verification codes — the OTPs and PINs that arrive by SMS bridge and then get retyped
// by hand into a browser.

// Code is one detected code and the evidence for it.
type Code struct {
	// Value is the code itself, exactly as it appeared.
	Value string
	// Label is the keyword that marks it as a code ("code", "otp", "קוד"), or empty for
	// a bare number in a short message.
	Label string
}

// fencePattern matches a fenced code block, including an unterminated one — a paste
// that runs to the end of the message is still a paste. (?s) so it spans lines.
var fencePattern = regexp.MustCompile("(?s)```.*?(```|$)")

// CodeRules is what counts as code-shaped: how long, and out of which characters.
type CodeRules struct {
	// MinLength and MaxLength bound the token in characters.
	MinLength, MaxLength int
	// Letters and Digits say which ASCII classes a code may contain (codes are ASCII in
	// practice, whatever language the message around them is in), and Symbols adds any
	// other characters it may contain — "-_" for a hyphenated code, "@!?" for a
	// generated password.
	Letters, Digits bool
	Symbols         string
	// RequireDigit demands at least one digit in the token.
	RequireDigit bool
}

// DefaultCodeRules is four to eight alphanumerics with at least one digit.
func DefaultCodeRules() CodeRules {
	return CodeRules{MinLength: 4, MaxLength: 8, Letters: true, Digits: true, RequireDigit: true}
}

// Describe is the rule set in words — "4–8 letters/digits with a digit" — for the
// status line to say what it was looking for when it found nothing.
func (r CodeRules) Describe() string {
	d := r.orDefault()
	length := fmt.Sprintf("%d–%d", d.MinLength, d.MaxLength)
	if d.MinLength == d.MaxLength {
		length = fmt.Sprintf("%d", d.MinLength)
	}
	var classes []string
	if d.Letters {
		classes = append(classes, "letters")
	}
	if d.Digits {
		classes = append(classes, "digits")
	}
	if d.Symbols != "" {
		classes = append(classes, strconv.Quote(d.Symbols))
	}
	out := length + " " + strings.Join(classes, "/")
	// Only worth saying where the token could otherwise hold none.
	if d.RequireDigit && (d.Letters || d.Symbols != "") {
		out += " with a digit"
	}
	return out
}

// Validate reports a rule set that could never match anything, which is a configuration
// mistake rather than a preference: silently finding no codes ever is indistinguishable
// from the feature being broken.
func (r CodeRules) Validate() error {
	switch {
	case r.MinLength < 1:
		return fmt.Errorf("min_length is %d, want at least 1", r.MinLength)
	case r.MaxLength < r.MinLength:
		return fmt.Errorf("max_length %d is below min_length %d", r.MaxLength, r.MinLength)
	case !r.Letters && !r.Digits && r.Symbols == "":
		return errors.New("no characters are allowed in a code — set letters, digits or symbols")
	case r.RequireDigit && !r.Digits:
		return errors.New("require_digit is on but digits are not allowed in a code")
	}
	return nil
}

// orDefault substitutes the built-in rules for a zero value, so every entry point can
// take a CodeRules without every caller having to have one.
func (r CodeRules) orDefault() CodeRules {
	if r.MinLength == 0 && r.MaxLength == 0 && !r.Letters && !r.Digits && r.Symbols == "" {
		return DefaultCodeRules()
	}
	return r
}

// allows reports whether c is a character a code may contain.
func (r CodeRules) allows(c rune) bool {
	switch {
	case r.Digits && c >= '0' && c <= '9':
		return true
	case r.Letters && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'):
		return true
	default:
		return r.Symbols != "" && strings.ContainsRune(r.Symbols, c)
	}
}

// tokens returns the byte ranges of the code-shaped runs in s: maximal runs of allowed
// characters whose length is in range.
func (r CodeRules) tokens(s string) [][2]int {
	var out [][2]int
	start, length := -1, 0
	for i, c := range s {
		if r.allows(c) {
			if start < 0 {
				start, length = i, 0
			}
			length++
			continue
		}
		out = r.appendToken(out, s, start, i, length)
		start, length = -1, 0
	}
	return r.appendToken(out, s, start, len(s), length)
}

// appendToken keeps a finished run if it is code-shaped.
func (r CodeRules) appendToken(out [][2]int, s string, start, end, length int) [][2]int {
	if start < 0 || length < r.MinLength || length > r.MaxLength {
		return out
	}
	if r.RequireDigit && !containsDigit(s[start:end]) {
		return out
	}
	return append(out, [2]int{start, end})
}

// codeLabels are the words that mark the number beside them as a code.
var codeLabels = []codeLabel{
	{word: "code"}, {word: "codes"}, {word: "otp"}, {word: "pin"}, {word: "pins"},
	{word: "verification"}, {word: "verify"}, {word: "password"}, {word: "passwords"},
	{word: "passcode"}, {word: "2fa"},
	{word: "קוד", prefixed: true}, {word: "סיסמה", prefixed: true}, {word: "אימות", prefixed: true},
}

// codeLabel is one keyword and how its language attaches to it.
type codeLabel struct {
	word string
	// prefixed allows a single attached letter in front of the word.
	prefixed bool
}

const (
	// labelWindow is how far from a token a keyword still counts, in runes.
	labelWindow = 40
	// afterLead is how far into a message a token may start and still be vouched for by
	// a keyword that comes *after* it.
	afterLead = 16
	// bareContextRunes is how much *other* text a message may hold for a bare,
	// unlabeled number in it to still count as a code — non-space runes, everything
	// except the number itself.
	bareContextRunes = 2
	// looseLimit caps how many candidates the on-request fallback offers.
	looseLimit = 8
	// maxScan caps how much of a body is examined.
	maxScan = 4096
)

// Codes returns the verification codes in body, strongest first and without duplicates:
// the ones a keyword vouches for, then — only in a message that is nothing but a code —
// bare numbers.
func Codes(body string, rules CodeRules) []Code {
	rules = rules.orDefault()
	body = trimScan(body)
	// Fenced blocks and links come out first, because both are full of code-shaped runs
	// that are not codes.
	scan := linkPattern.ReplaceAllString(fencePattern.ReplaceAllString(body, " "), " ")
	lower := strings.ToLower(scan)
	tokens := rules.tokens(scan)
	if len(tokens) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(tokens))
	out := make([]Code, 0, len(tokens))
	// Three passes, strongest evidence first, because the order is what a single
	// keystroke acts on.
	for _, loc := range tokens {
		token := scan[loc[0]:loc[1]]
		if seen[token] || partOfBiggerNumber(scan, loc[0], loc[1]) {
			continue
		}
		if label := labelIn(windowBefore(lower, loc[0])); label != "" {
			seen[token] = true
			out = append(out, Code{Value: token, Label: label})
		}
	}
	for _, loc := range tokens {
		token := scan[loc[0]:loc[1]]
		if seen[token] || partOfBiggerNumber(scan, loc[0], loc[1]) ||
			utf8.RuneCountInString(scan[:loc[0]]) > afterLead {
			continue
		}
		if label := labelIn(windowAfter(lower, loc[1])); label != "" {
			seen[token] = true
			out = append(out, Code{Value: token, Label: label})
		}
	}
	// Unlabeled, so the bar is higher on three counts: all digits (a bare alphanumeric
	// run is far more often a word with a digit in it — "sha1", "v2beta", "utf8"),
	// essentially alone in the message, and the *only* candidate.
	if soleDigitToken(scan, tokens) {
		for _, loc := range tokens {
			token := scan[loc[0]:loc[1]]
			if seen[token] || !allDigits(token) || !standsAlone(scan, loc[0], loc[1]) {
				continue
			}
			if partOfBiggerNumber(scan, loc[0], loc[1]) {
				continue
			}
			seen[token] = true
			out = append(out, Code{Value: token})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CodeCandidates answers a different question from Codes, and so has a different bar:
// somebody has pressed the key on this specific message, so the only useful answer is
// the most plausible thing in it.
func CodeCandidates(body string, rules CodeRules) []Code {
	if found := Codes(body, rules); len(found) > 0 {
		return found
	}
	rules = rules.orDefault()
	scan := linkPattern.ReplaceAllString(fencePattern.ReplaceAllString(trimScan(body), " "), " ")
	out := make([]Code, 0, looseLimit)
	seen := make(map[string]bool)
	for _, loc := range rules.tokens(scan) {
		token := scan[loc[0]:loc[1]]
		if seen[token] || partOfBiggerNumber(scan, loc[0], loc[1]) {
			continue
		}
		seen[token] = true
		out = append(out, Code{Value: token})
		if len(out) == looseLimit {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// trimScan caps how much of a body is looked at, on a rune boundary.
func trimScan(body string) string {
	if len(body) <= maxScan {
		return body
	}
	cut := maxScan
	for cut > 0 && !utf8.RuneStart(body[cut]) {
		cut--
	}
	return body[:cut]
}

// labelIn returns the first code keyword appearing as a word in an already-lowercased
// window of text around a token, or empty if none does.
func labelIn(window string) string {
	for _, label := range codeLabels {
		if containsWord(window, label) {
			return label.word
		}
	}
	return ""
}

// containsWord reports whether the label appears in haystack as its own word.
func containsWord(haystack string, label codeLabel) bool {
	for from := 0; from <= len(haystack)-len(label.word); {
		at := strings.Index(haystack[from:], label.word)
		if at < 0 {
			return false
		}
		at += from
		if standsAsWord(haystack, at, at+len(label.word), label.prefixed) {
			return true
		}
		from = at + 1
	}
	return false
}

// standsAsWord reports whether haystack[at:end] is a word of its own — nothing
// alphanumeric on either side — or, when the label allows a prefix, a word carrying
// exactly one attached letter in front.
func standsAsWord(haystack string, at, end int, prefixed bool) bool {
	// A letter or digit after it makes this a longer, different word, whatever the
	// language: this is what separates סיסמה in הסיסמה (accepted) from קוד in הנקודה
	// (rejected).
	if alphanumericAt(haystack, end, +1) {
		return false
	}
	if !alphanumericAt(haystack, at-1, -1) {
		return true
	}
	if !prefixed {
		return false
	}
	// One attached letter, and only if the word starts there.
	return !alphanumericAt(haystack, prevRuneStart(haystack, at)-1, -1)
}

// prevRuneStart is the byte offset of the rune ending at i.
func prevRuneStart(s string, i int) int {
	for i--; i > 0 && !utf8.RuneStart(s[i]); i-- {
	}
	return i
}

// alphanumericAt reports whether there is a letter or digit at byte offset i. dir says
// which rune is meant: -1 for the one ending at i, +1 for the one beginning there.
func alphanumericAt(s string, i, dir int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	if dir < 0 {
		for i > 0 && !utf8.RuneStart(s[i]) {
			i--
		}
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// windowBefore is the labelWindow runes of text just before a token.
func windowBefore(s string, start int) string {
	from := start
	for range labelWindow {
		if from == 0 {
			break
		}
		_, size := utf8.DecodeLastRuneInString(s[:from])
		from -= size
	}
	return s[from:start]
}

// windowAfter is the labelWindow runes of text just after a token.
func windowAfter(s string, end int) string {
	to := end
	for range labelWindow {
		if to == len(s) {
			break
		}
		_, size := utf8.DecodeRuneInString(s[to:])
		to += size
	}
	return s[end:to]
}

// partOfBiggerNumber reports whether the token is a piece of a longer number rather
// than a number of its own — glued to more digits by a separator.
func partOfBiggerNumber(scan string, start, end int) bool {
	return glued(scan, start-1, -1) || glued(scan, end, +1)
}

// glued reports whether the byte at i is a numeric separator with a digit beyond it.
func glued(scan string, i, dir int) bool {
	if i < 0 || i >= len(scan) {
		return false
	}
	switch scan[i] {
	case '-', '.', '/', '+':
	default:
		return false
	}
	beyond := i + dir
	if beyond < 0 || beyond >= len(scan) {
		return false
	}
	return scan[beyond] >= '0' && scan[beyond] <= '9'
}

// soleDigitToken reports whether exactly one of the code-shaped tokens is all digits.
func soleDigitToken(scan string, tokens [][2]int) bool {
	count := 0
	for _, loc := range tokens {
		if allDigits(scan[loc[0]:loc[1]]) {
			count++
			if count > 1 {
				return false
			}
		}
	}
	return count == 1
}

// standsAlone reports whether the token at [start,end) is essentially the whole
// message — everything else in it, spaces aside, fits in bareContextRunes.
func standsAlone(scan string, start, end int) bool {
	rest := 0
	for _, r := range scan[:start] + scan[end:] {
		if !unicode.IsSpace(r) {
			rest++
			if rest > bareContextRunes {
				return false
			}
		}
	}
	return true
}

// containsDigit reports whether s has a digit in it.
func containsDigit(s string) bool {
	return strings.ContainsAny(s, "0123456789")
}

// allDigits reports whether s is nothing but digits.
func allDigits(s string) bool {
	return strings.TrimLeft(s, "0123456789") == ""
}
