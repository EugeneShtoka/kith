package domain

import (
	"strings"
	"testing"
)

// values is the codes' values, for the cases where the evidence is not what is being
// checked.
func values(codes []Code) []string {
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		out = append(out, c.Value)
	}
	return out
}

// The shapes these actually arrive in. Every case here is a message a bridge or a
// service really sends, which is the only reason to believe the detector works.
func TestCodes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, body string
		want       []string
	}{
		{"labeled", "Your verification code is 482910", []string{"482910"}},
		{"colon", "Code: 482910", []string{"482910"}},
		{"google prefix", "G-482910 is your Google verification code", []string{"482910"}},
		{"keyword after", "482910 is your login code", []string{"482910"}},
		{"bare in a short message", "482910", []string{"482910"}},
		{"alphanumeric code", "Your code is A1B2C3", []string{"A1B2C3"}},
		{"four digit pin", "Your PIN is 4821", []string{"4821"}},
		{"two codes, in order", "code 482910 or use pin 7391", []string{"482910", "7391"}},
		{"the same code twice", "Code 482910. Enter 482910 to continue.", []string{"482910"}},

		// Rejections.
		{"no code", "see you at the standup", nil},
		{"expired", "Your code is expired, request a new one", nil},
		{"phone number", "call me on 0521234567", nil},
		{"long digits", "order 1234567890123 has shipped", nil},
		{"a bare number in prose", "we shipped 12345 units to the warehouse and the invoice is still pending review", nil},
		{"a year", "the 2026 planning doc is up", nil},
		{"empty", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := values(Codes(tc.body, CodeRules{}))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("Codes(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// Bridged Hebrew SMS is the daily case this was written for, so it is tested in the
// script it arrives in rather than assumed to fall out of the English rules.
func TestCodesHebrew(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, body, want string
	}{
		{"code", "קוד האימות שלך הוא 482910", "482910"},
		{"password", "הסיסמה החד-פעמית שלך: 7391", "7391"},
		{"code after the number", "482910 הוא קוד האימות שלך", "482910"},
		{"mixed script", "Acme Bank: קוד 482910", "482910"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := values(Codes(tc.body, CodeRules{}))
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("Codes(%q) = %v, want [%s]", tc.body, got, tc.want)
			}
		})
	}
}

// A URL is full of code-shaped runs, so links come out before anything is looked at.
// Without this, a shortened link's path is a perfectly good six-character "code".
func TestCodesIgnoresLinks(t *testing.T) {
	t.Parallel()

	got := values(Codes("Log in at https://ex.ample/a1b2c3 with code 482910", CodeRules{}))
	if len(got) != 1 || got[0] != "482910" {
		t.Errorf("Codes = %v, want just the code", got)
	}
	if bare := Codes("https://ex.ample/a1b2c3", CodeRules{}); bare != nil {
		t.Errorf("a link alone yielded %v", bare)
	}
}

// The evidence is kept, not collapsed into a bool: it is what the UI shows to say
// *why* something is thought to be a code.
func TestCodesReportTheirEvidence(t *testing.T) {
	t.Parallel()

	labeled := Codes("Your code is 482910", CodeRules{})
	if len(labeled) != 1 || labeled[0].Label != "code" {
		t.Fatalf("labeled = %+v, want the keyword kept", labeled)
	}
	bare := Codes("482910", CodeRules{})
	if len(bare) != 1 || bare[0].Label != "" {
		t.Fatalf("bare = %+v, want no label", bare)
	}
}

// Strongest evidence first, because a single keystroke takes the first one.
func TestCodesRankEvidenceNotPosition(t *testing.T) {
	t.Parallel()

	got := values(Codes("7391 people voted. Your code is 482910", CodeRules{}))
	if len(got) == 0 || got[0] != "482910" {
		t.Errorf("Codes = %v, want the labeled code first", got)
	}
}

// With no keyword anywhere, the message must *be* the code.
func TestCodesBareNeedsAMessageThatIsOnlyTheCode(t *testing.T) {
	t.Parallel()

	for _, body := range []string{"482910", "  482910  ", "G-482910", "482910 ✅"} {
		if got := values(Codes(body, CodeRules{})); len(got) != 1 || got[0] != "482910" {
			t.Errorf("Codes(%q) = %v, want just the code", body, got)
		}
	}
	for _, body := range []string{
		"the 2026 planning doc is up",
		"we shipped 12345 units to the warehouse",
		// These shapes are what a loose rule actually let through when it was run over
		// a real message history: a name with a number after it, a ticket number, an id
		// in backticks, a year.
		"Table 5512",
		"3120 is done on the frontend",
		"try 9042 ?",
		"record id `27553901`",
		"הסרט 1994",
		// Several numbers and no keyword: which one is the code is unknowable, and this
		// shape is a card, an account or a date rather than an OTP.
		"1234 5678 9012 3456",
		"482910 7391",
	} {
		if got := Codes(body, CodeRules{}); got != nil {
			t.Errorf("Codes(%q) = %+v, want nothing", body, got)
		}
	}
}

// A keyword only counts as a whole word.
func TestCodesKeywordsAreWords(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		"the spin off left 4821 rows behind",
		"we should encode the 4821 differently",
		// Hebrew is where this bites hardest: קוד sits inside הנקודה (the point) and
		// פקודה (command), both entirely ordinary words.
		"זאת הנקודה — יש 3120 שורות בטבלה",
		"הפקודה הזאת מחזירה 4821 שורות",
	} {
		if got := Codes(body, CodeRules{}); got != nil {
			t.Errorf("Codes(%q) = %+v, want nothing", body, got)
		}
	}
	// …while the Hebrew forms that *do* mean it keep working: a one-letter prefix is
	// how the language says "the".
	for _, body := range []string{"הסיסמה שלך: 7391", "הקוד הוא 7391", "וקוד האימות 7391"} {
		if got := values(Codes(body, CodeRules{})); len(got) != 1 || got[0] != "7391" {
			t.Errorf("Codes(%q) = %v, want [7391]", body, got)
		}
	}
	// Plurals are listed explicitly, so they work without letting "pink" in.
	if got := values(Codes("your verification codes are 482910 and 7391", CodeRules{})); len(got) != 2 {
		t.Errorf("plural = %v, want both", got)
	}
}

// A fenced paste is source code and logs, not a message about a code.
func TestCodesIgnoreFencedBlocks(t *testing.T) {
	t.Parallel()

	body := "```\n[server] 55120 - INFO [loader] code dependencies initialized\n```"
	if got := Codes(body, CodeRules{}); got != nil {
		t.Errorf("Codes(fenced) = %+v, want nothing", got)
	}
	// An unterminated fence is still a paste.
	if got := Codes("look:\n```\nVERBOSE=true code 90421", CodeRules{}); got != nil {
		t.Errorf("Codes(unterminated fence) = %+v, want nothing", got)
	}
	// Prose outside the fence still counts.
	if got := values(Codes("your code is 482910\n```\nlog 81561\n```", CodeRules{})); len(got) != 1 || got[0] != "482910" {
		t.Errorf("Codes = %v, want just the code outside the fence", got)
	}
}

// The two questions have different bars.
func TestCodeCandidatesFallBackOnRequest(t *testing.T) {
	t.Parallel()

	// Nothing worth volunteering, but an obvious answer once asked.
	if got := Codes("the 2026 planning doc is up", CodeRules{}); got != nil {
		t.Errorf("unprompted = %+v, want nothing", got)
	}
	if got := values(CodeCandidates("the 2026 planning doc is up", CodeRules{})); len(got) != 1 || got[0] != "2026" {
		t.Errorf("on request = %v, want [2026]", got)
	}
	// A keyword-backed answer is never diluted with weaker ones.
	otp := "your code to complete the purchase is 194827, card ending 1666"
	strict, asked := values(Codes(otp, CodeRules{})), values(CodeCandidates(otp, CodeRules{}))
	if len(strict) != 1 || strict[0] != "194827" {
		t.Fatalf("unprompted = %v, want just the code", strict)
	}
	if strings.Join(asked, ",") != strings.Join(strict, ",") {
		t.Errorf("on request = %v, want the same single answer as %v", asked, strict)
	}
	// With nothing code-shaped at all, both still say nothing.
	if got := CodeCandidates("see you at the standup", CodeRules{}); got != nil {
		t.Errorf("on request = %+v, want nothing", got)
	}
	// The fallback is capped: a picker of thirty numbers is not a question.
	many := "5512 9042 3120 4821 7391 8906 1857 2026 1994 4444 5678"
	if got := CodeCandidates(many, CodeRules{}); len(got) != looseLimit {
		t.Errorf("candidates = %d, want the cap of %d", len(got), looseLimit)
	}
}

// Only the top of a body is scanned: a code is at the start of a message or it is not
// there, and an enormous paste should not become an enormous scan.
func TestCodesScanIsCapped(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("x", maxScan) + " your code is 482910"
	if got := Codes(body, CodeRules{}); got != nil {
		t.Errorf("past the cap = %+v, want nothing", got)
	}
}

// The shape is configurable because "a code" is a different shape on different
// services, and the defaults are measured against one person's history.
func TestCodeRulesShapeTheSearch(t *testing.T) {
	t.Parallel()

	exactlySix := CodeRules{MinLength: 6, MaxLength: 6, Letters: true, Digits: true, RequireDigit: true}
	if got := Codes("Your PIN is 4821", exactlySix); got != nil {
		t.Errorf("a four-digit PIN under a six-only rule = %+v, want nothing", got)
	}
	if got := values(Codes("Your code is 482910", exactlySix)); len(got) != 1 {
		t.Errorf("a six-digit code under a six-only rule = %v", got)
	}

	// Digits only: a letter is a boundary, so an alphanumeric code breaks into runs too
	// short to qualify.
	digitsOnly := CodeRules{MinLength: 4, MaxLength: 8, Digits: true, RequireDigit: true}
	if got := Codes("Your code is A1B2C3", digitsOnly); got != nil {
		t.Errorf("an alphanumeric code under a digits-only rule = %+v, want nothing", got)
	}
	if got := values(Codes("Your code is 482910", digitsOnly)); len(got) != 1 {
		t.Errorf("digits-only rule missed a digit code: %v", got)
	}

	// Letters only, for a service whose codes have no digits in them at all.
	lettersOnly := CodeRules{MinLength: 4, MaxLength: 8, Letters: true}
	if got := values(Codes("Your code is ZXCVBN", lettersOnly)); len(got) == 0 || got[0] != "ZXCVBN" {
		t.Errorf("letters-only rule = %v, want ZXCVBN first", got)
	}

	// Symbols extend the run rather than ending it, so a hyphenated code stays whole.
	hyphenated := CodeRules{MinLength: 4, MaxLength: 10, Letters: true, Digits: true, Symbols: "-", RequireDigit: true}
	if got := values(Codes("Your code is A1-B2-C3", hyphenated)); len(got) != 1 || got[0] != "A1-B2-C3" {
		t.Errorf("hyphenated rule = %v, want [A1-B2-C3]", got)
	}
	// …and without the symbol allowed, the same body is three runs too short to count.
	if got := Codes("Your code is A1-B2-C3", DefaultCodeRules()); got != nil {
		t.Errorf("default rules on a hyphenated code = %+v, want nothing", got)
	}
}

// Turning the digit requirement off is a real loosening, and the test says so out loud
// rather than leaving the consequence to be discovered.
func TestCodeRulesWithoutTheDigitRequirement(t *testing.T) {
	t.Parallel()

	loose := CodeRules{MinLength: 4, MaxLength: 8, Letters: true, Digits: true}
	if got := values(Codes("Your code is expired", loose)); len(got) == 0 || got[0] != "expired" {
		t.Errorf("Codes = %v — without require_digit this is expected to hand over a word", got)
	}
	if got := Codes("Your code is expired", DefaultCodeRules()); got != nil {
		t.Errorf("with the requirement on = %+v, want nothing", got)
	}
}

// A rule set that could never match is a configuration mistake, not a preference:
// finding no codes ever is indistinguishable from the feature being broken.
func TestCodeRulesValidate(t *testing.T) {
	t.Parallel()

	if err := DefaultCodeRules().Validate(); err != nil {
		t.Fatalf("the defaults do not validate: %v", err)
	}
	for name, rules := range map[string]CodeRules{
		"no characters":    {MinLength: 4, MaxLength: 8},
		"inverted range":   {MinLength: 9, MaxLength: 4, Digits: true},
		"zero minimum":     {MinLength: 0, MaxLength: 8, Digits: true},
		"digit impossible": {MinLength: 4, MaxLength: 8, Letters: true, RequireDigit: true},
	} {
		if err := rules.Validate(); err == nil {
			t.Errorf("%s: validated, want an error", name)
		}
	}
	// Symbols alone are a legitimate, if odd, alphabet.
	if err := (CodeRules{MinLength: 4, MaxLength: 8, Symbols: "@!?"}).Validate(); err != nil {
		t.Errorf("symbols-only rules: %v", err)
	}
}

// The zero value means the defaults, so a caller with no configuration still gets
// sensible behavior rather than a rule that admits nothing.
func TestCodeRulesZeroValueMeansDefaults(t *testing.T) {
	t.Parallel()

	if got := values(Codes("Your code is 482910", CodeRules{})); len(got) != 1 {
		t.Errorf("zero rules found %v", got)
	}
	if got := values(CodeCandidates("the 2026 planning doc is up", CodeRules{})); len(got) != 1 {
		t.Errorf("zero rules on request found %v", got)
	}
}

// The status line says what a code has to look like when it finds none, so the
// description has to read as English and follow the rules it describes — a fixed length
// says one number, and a class the rules exclude must not be advertised.
func TestCodeRulesDescribe(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		rules CodeRules
		want  string
	}{
		"the defaults":   {DefaultCodeRules(), "4–8 letters/digits with a digit"},
		"the zero value": {CodeRules{}, "4–8 letters/digits with a digit"},
		// "6 digits with a digit" would be a tautology; the requirement is only worth
		// stating where the charset admits something that is not one.
		"a fixed length, digits only": {
			CodeRules{MinLength: 6, MaxLength: 6, Digits: true, RequireDigit: true},
			"6 digits",
		},
		"letters only": {
			CodeRules{MinLength: 4, MaxLength: 10, Letters: true},
			"4–10 letters",
		},
		"with symbols": {
			CodeRules{MinLength: 8, MaxLength: 12, Letters: true, Digits: true, Symbols: "-_"},
			`8–12 letters/digits/"-_"`,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := tc.rules.Describe(); got != tc.want {
				t.Errorf("Describe() = %q, want %q", got, tc.want)
			}
		})
	}
}
