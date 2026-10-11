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

// Code is one detected verification code (OTP, PIN) and the evidence for it.
type Code struct {
	// Value is the code exactly as it appeared.
	Value string
	// Label is the keyword marking it ("code", "otp", "קוד"); empty for a bare number.
	Label string
}

// fencePattern matches a fenced code block, including an unterminated one.
var fencePattern = regexp.MustCompile("(?s)```.*?(```|$)")

// CodeRules is what counts as code-shaped: length and character set.
type CodeRules struct {
	// MinLength and MaxLength bound the token in characters.
	MinLength, MaxLength int
	// Letters and Digits allow ASCII classes (codes are ASCII in practice); Symbols adds
	// other allowed characters, e.g. "-_".
	Letters, Digits bool
	Symbols         string
	RequireDigit    bool
}

// DefaultCodeRules is four to eight alphanumerics with at least one digit.
func DefaultCodeRules() CodeRules {
	return CodeRules{MinLength: 4, MaxLength: 8, Letters: true, Digits: true, RequireDigit: true}
}

// Describe is the rule set in words: "4–8 letters/digits with a digit".
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
	// Only worth saying when the token could otherwise hold none.
	if d.RequireDigit && (d.Letters || d.Symbols != "") {
		out += " with a digit"
	}
	return out
}

// Validate rejects a rule set that could never match, which would look like a broken
// feature.
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

// orDefault substitutes the built-in rules for a zero value.
func (r CodeRules) orDefault() CodeRules {
	if r.MinLength == 0 && r.MaxLength == 0 && !r.Letters && !r.Digits && r.Symbols == "" {
		return DefaultCodeRules()
	}
	return r
}

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

// tokens returns the byte ranges of maximal allowed-character runs of valid length.
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

// codeLabels are the keywords that mark a nearby token as a code.
var codeLabels = []codeLabel{
	{word: "code"}, {word: "codes"}, {word: "otp"}, {word: "pin"}, {word: "pins"},
	{word: "verification"}, {word: "verify"}, {word: "password"}, {word: "passwords"},
	{word: "passcode"}, {word: "2fa"},
	{word: "קוד", prefixed: true}, {word: "סיסמה", prefixed: true}, {word: "אימות", prefixed: true},
}

type codeLabel struct {
	word string
	// prefixed allows one attached letter in front (Hebrew prefixes).
	prefixed bool
}

const (
	// labelWindow is how far from a token a keyword still counts, in runes.
	labelWindow = 40
	// afterLead is how far into a message (runes) a token may start and still be
	// labeled by a keyword after it.
	afterLead = 16
	// bareContextRunes is how many other non-space runes a message may hold for a bare
	// number in it to count as a code.
	bareContextRunes = 2
	// looseLimit caps how many candidates the on-request fallback offers.
	looseLimit = 8
	// maxScan caps how much of a body is examined.
	maxScan = 4096
)

// Codes returns the verification codes in body, strongest first, deduplicated: those a
// keyword labels, then a bare number in a message that is nothing but that number.
func Codes(body string, rules CodeRules) []Code {
	rules = rules.orDefault()
	body = trimScan(body)
	// Fenced blocks and links are full of code-shaped runs that are not codes.
	scan := linkPattern.ReplaceAllString(fencePattern.ReplaceAllString(body, " "), " ")
	lower := strings.ToLower(scan)
	tokens := rules.tokens(scan)
	if len(tokens) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(tokens))
	out := make([]Code, 0, len(tokens))
	// Three passes, strongest evidence first: a single keystroke takes the first.
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
	// Unlabeled: must be all digits (not "sha1", "utf8"), essentially alone in the
	// message, and the only candidate.
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

// CodeCandidates is Codes with a lower bar, for an explicit request on one message:
// failing Codes, any code-shaped tokens.
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

// trimScan caps the body at maxScan bytes, on a rune boundary.
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

// labelIn returns the first code keyword appearing as a word in a lowercased window.
func labelIn(window string) string {
	for _, label := range codeLabels {
		if containsWord(window, label) {
			return label.word
		}
	}
	return ""
}

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

// standsAsWord reports whether haystack[at:end] is a whole word, or, if prefixed, one
// with exactly one attached letter in front.
func standsAsWord(haystack string, at, end int, prefixed bool) bool {
	// A letter or digit after makes a different word: סיסמה in הסיסמה is accepted,
	// קוד in הנקודה is not.
	if alphanumericAt(haystack, end, +1) {
		return false
	}
	if !alphanumericAt(haystack, at-1, -1) {
		return true
	}
	if !prefixed {
		return false
	}
	return !alphanumericAt(haystack, prevRuneStart(haystack, at)-1, -1)
}

// prevRuneStart is the byte offset of the rune ending at i.
func prevRuneStart(s string, i int) int {
	for i--; i > 0 && !utf8.RuneStart(s[i]); i-- {
	}
	return i
}

// alphanumericAt reports a letter or digit at byte offset i: the rune ending there
// (dir -1) or starting there (dir +1).
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

// partOfBiggerNumber reports whether a separator glues the token to more digits.
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

// standsAlone reports whether everything but the token fits in bareContextRunes
// non-space runes.
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

func containsDigit(s string) bool {
	return strings.ContainsAny(s, "0123456789")
}

func allDigits(s string) bool {
	return strings.TrimLeft(s, "0123456789") == ""
}
