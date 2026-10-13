package domain

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// What the letters say, message by message: the script is exact, telltale letters
// narrow, and what they cannot settle stays a list.
func TestLanguageOfReadsTheLetters(t *testing.T) {
	t.Parallel()

	for body, want := range map[string]Language{
		"你好":                   {Script: "Han", Suspects: []string{"zh", "ja"}, Likely: "zh"},
		"加微信123，送红包😀":          {Script: "Han", Suspects: []string{"zh", "ja"}, Likely: "zh"},
		"好!":                   {Script: "Han", Suspects: []string{"zh", "ja"}, Likely: "zh"},
		"東京に行きます":              {Script: "Kana", Suspects: []string{"ja"}, Likely: "ja"},
		"カタカナ":                 {Script: "Kana", Suspects: []string{"ja"}, Likely: "ja"},
		"大韓民國 만세":              {Script: "Hangul", Suspects: []string{"ko"}, Likely: "ko"},
		"สวัสดีครับ":           {Script: "Thai", Suspects: []string{"th"}, Likely: "th"},
		"שלום לכולם":           {Script: "Hebrew", Suspects: []string{"he", "yi"}, Likely: "he"},
		"Straße":               {Script: "Latin", Suspects: []string{"de"}, Likely: "de"},
		"Xin chào, bạn khỏe":   {Script: "Latin", Suspects: []string{"vi"}, Likely: "vi"},
		"Вы где?":              {Script: "Cyrillic", Suspects: []string{"ru", "be"}},
		"бонусът изтича":       {Script: "Cyrillic", Suspects: []string{"ru", "bg"}},
		"Привіт, їду":          {Script: "Cyrillic", Suspects: []string{"uk"}, Likely: "uk"},
		"Ok, see you tomorrow": {Script: "Latin", Suspects: writingSystems[0].languages},
		"I live in Zürich":     {Script: "Latin", Suspects: writingSystems[0].languages},
		// Each word rules the other's language out, so neither decides.
		"Straße mañana":              {Script: "Latin", Suspects: writingSystems[0].languages},
		"東京都知事選挙の結果":                 {Script: "Kana", Suspects: []string{"ja"}, Likely: "ja"},
		"Тогда с вас вилки тарелки?": {Script: "Cyrillic", Suspects: writingSystems[1].languages},
		// Chinese characters outnumber the one English word, counted a character each.
		"hello 你好世界朋友": {Script: "Han", Suspects: []string{"zh", "ja"}, Likely: "zh"},
		// Not language: links, IDs, shortcodes, code, faces, digits, emoji.
		"https://例子.cn/你好 ok": {Script: "Latin", Suspects: writingSystems[0].languages},
		"`你好世界` ok":           {Script: "Latin", Suspects: writingSystems[0].languages},
		"¯\\_(ツ)_/¯":          {},
		"(ノ°Д°)ノ":             {},
		"@用户:example.org 😀":   {},
		":你好: 12:59 !!!":      {},
		"":                    {},
	} {
		if got := LanguageOf(body); !sameLanguage(got, want) {
			t.Errorf("LanguageOf(%q) = %+v, want %+v", body, got, want)
		}
	}
}

func sameLanguage(a, b Language) bool {
	return a.Script == b.Script && a.Likely == b.Likely && slices.Equal(a.Suspects, b.Suspects)
}

// The completion model is told the language in words; Latin letters alone are English.
func TestLanguageNames(t *testing.T) {
	t.Parallel()

	for body, want := range map[string]string{
		"the quick brown fox": "English",
		"שלום לכולם":          "Hebrew",
		"привет всем":         "Russian",
		"Straße":              "German",
		"你好":                  "Chinese",
		"東京に行きます":             "Japanese",
		"1234 ?!":             "",
	} {
		if got := LanguageOf(body).Name(); got != want {
			t.Errorf("LanguageOf(%q).Name() = %q, want %q", body, got, want)
		}
	}
}

// Rule entries: codes, families and regions expand; anything else is refused, never
// read as an empty list.
func TestLanguageCodes(t *testing.T) {
	t.Parallel()

	got, err := LanguageCodes([]string{" ZH ", "region:asia-east", "family:Slavic"})
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"zh", "ja", "ko", "mn", "ru", "pl"} {
		if !slices.Contains(got, code) {
			t.Errorf("LanguageCodes = %v, missing %s", got, code)
		}
	}
	if strings.Count(strings.Join(got, " "), "zh") != 1 {
		t.Errorf("LanguageCodes = %v, zh twice", got)
	}
	for _, entry := range []string{"", "russian", "family:", "family:asian", "region:asia", "script:Han"} {
		if _, err := LanguageCodes([]string{entry}); err == nil {
			t.Errorf("LanguageCodes(%q) accepted", entry)
		}
	}
	for code := range languageNames {
		if _, err := LanguageCodes([]string{code}); err != nil {
			t.Errorf("%s: %v", code, err)
		}
	}
	// Every language a script, a family or a region names is one a rule can name.
	for _, ws := range writingSystems {
		for _, code := range ws.languages {
			if languageNames[code] == "" {
				t.Errorf("script %s: %s has no name", ws.name, code)
			}
		}
	}
	for _, sets := range []map[string][]string{languageFamilies, languageRegions} {
		for name, codes := range sets {
			for _, code := range codes {
				if languageNames[code] == "" {
					t.Errorf("%s: %s has no name", name, code)
				}
			}
		}
	}
	for _, tt := range telltales {
		for _, code := range tt.languages {
			if languageNames[code] == "" {
				t.Errorf("telltale %s: %s has no name", tt.letters, code)
			}
		}
	}
	if s, err := ScriptNamed(" han "); err != nil || s != "Han" {
		t.Errorf("ScriptNamed(han) = %q, %v", s, err)
	}
	if _, err := ScriptNamed("Chinese"); err == nil {
		t.Error("ScriptNamed(Chinese) accepted")
	}
}

// The noise a chat message carries that is no language: whatever script is inside a
// link, an ID, a shortcode, code or a face must not decide anything.
var languageNoise = []string{
	"https://例子.cn/你好", "www.example.jp/日本語", "@用户:example.org", "#频道:example.org",
	"!房间:example.org", ":你好:", "`你好世界`", "`東京に行きます`", "```\nこんにちは 안녕\n```",
	"¯\\_(ツ)_/¯", "(ツ)", "ツ", "シ", "(ノ°Д°)ノ", "(╯°□°)╯︵ ┻━┻", "ʕ•ᴥ•ʔ", "(｡◕‿◕｡)",
	"😀", "👍🏽", "🇧🇬", "123", "12:59", "!!!", "...", "—", "+359 88 000 0000",
}

// Words this account's people write (English, Russian, Hebrew), the short and the
// ambiguous most of all: "Да" and "Ok" are what statistical identifiers got wrong.
var languageMine = []string{
	"ok", "Ok", "I", "a", "see", "you", "tomorrow", "don't", "thanks", "lol", "sure",
	"Да", "да", "Нет", "я", "в", "с", "Тогда", "вас", "вилки", "тарелки", "Вы", "съёмки",
	"объявление", "щас", "Привет", "шо",
	"שלום", "כן", "לא", "ב", "מה",
}

// Loanwords: written in your languages with a letter from another (ü, ñ, ß, ё in a
// transliteration). Fewer than half the words in a message, they decide nothing.
var languageLoans = []string{"Zürich", "jalapeño", "Straße", "façade", "Ελλάδα", "naïve", "Đà", "Ђоковић"}

// Words in Chinese, Japanese, Korean and Thai, by the script that settles them.
var languageForeign = map[string][]string{
	"Han":    {"你好", "加微信", "朋友", "好", "送红包", "点击链接"},
	"Kana":   {"東京に行きます", "こんにちは", "ありがとう", "カタカナ"},
	"Hangul": {"안녕하세요", "만세", "감사합니다"},
	"Thai":   {"สวัสดี", "ขอบคุณ"},
}

// generated joins words and noise, with punctuation and emoji stuck to some words, and
// reports how many words it used.
func generated(rng *rand.Rand, words []string, n int) (string, int) {
	var parts []string
	worded := 0
	for range n {
		if rng.IntN(3) == 0 {
			parts = append(parts, languageNoise[rng.IntN(len(languageNoise))])
			continue
		}
		worded++
		parts = append(parts, words[rng.IntN(len(words))]+[]string{"", "", "!", "?", "...", "😀", "，"}[rng.IntN(7)])
	}
	return strings.Join(parts, []string{" ", "\n", "  "}[rng.IntN(3)]), worded
}

// The safety property `not_lang` rests on: a message written only in your languages,
// whatever noise it carries, is never read as outside them, never as in an East Asian
// language, and never as one of their scripts. And its converse: a message in one of
// those scripts alone, with the same noise, always is.
func TestLanguageRulesOverGeneratedMessages(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewPCG(1, 13))
	mine := []string{"en", "ru", "he"}
	asia, err := LanguageCodes([]string{"region:asia-east", "region:asia-southeast"})
	if err != nil {
		t.Fatal(err)
	}
	for range 20000 {
		body, worded := generated(rng, languageMine, 1+rng.IntN(9))
		// Fewer loanwords than a third of your words: "I live in Zürich".
		for range rng.IntN((worded-1)/3 + 1) {
			body += " " + languageLoans[rng.IntN(len(languageLoans))]
		}
		lang := LanguageOf(body)
		if lang.Outside(mine) || lang.In(asia) || slices.Contains([]string{"Han", "Kana", "Hangul", "Thai"}, lang.Script) {
			t.Fatalf("mine %q read as %+v", body, lang)
		}
		if worded > 0 && len(lang.Suspects) == 0 {
			t.Fatalf("mine %q has words but no language", body)
		}
	}
	for script, words := range languageForeign {
		for range 5000 {
			body, worded := generated(rng, words, 1+rng.IntN(6))
			if worded == 0 {
				continue
			}
			lang := LanguageOf(body)
			if lang.Script != script || !lang.Outside(mine) {
				t.Fatalf("%s %q read as %+v", script, body, lang)
			}
			if script != "Thai" && !lang.In(asia) {
				t.Fatalf("%s %q not in East Asia: %+v", script, body, lang)
			}
		}
	}
	for range 5000 {
		body, _ := generated(rng, languageNoise, 1+rng.IntN(6))
		if lang := LanguageOf(body); len(lang.Suspects) > 0 {
			t.Fatalf("noise %q read as %+v", body, lang)
		}
	}
}

// A filter's language clauses: a language alone is a filter, and with words or a
// sender every clause must hold.
func TestFiltersCatchByLanguage(t *testing.T) {
	t.Parallel()

	room := RoomFacts{ID: "!r:x"}
	notMine := Filter{Name: "not mine", Tag: "Spam", NotLang: []string{"en", "ru", "he"}}
	chinese := Filter{Name: "chinese", Tag: "Spam", Lang: []string{"zh"}}
	kana := Filter{Name: "kana", Tag: "Spam", Scripts: []string{"Kana"}}
	promo := Filter{Name: "promo", Tag: "Spam", Words: Tracked{Words: []string{"*бонус*"}}, NotLang: []string{"ru"}}
	for _, tc := range []struct {
		f    Filter
		body string
		want bool
	}{
		{notMine, "你好", true},
		{notMine, "東京に行きます", true},
		{notMine, "Да", false},
		{notMine, "Ok", false},
		{notMine, "Здравейте", false}, // could be Russian
		{notMine, "😀", false},
		{notMine, "¯\\_(ツ)_/¯", false},
		{chinese, "你好", true},
		{chinese, "東京に行きます", false},
		{kana, "東京に行きます", true},
		{kana, "你好", false},
		{promo, "бонусът изтича", false}, // ъ: Russian or Bulgarian, so not outside Russian
		{promo, "Xin chào bonus", false}, // outside Russian, but no word caught
		{promo, "Вашият бонус е", false},
	} {
		if got := tc.f.Catches(tc.body, "@x:y", room); got != tc.want {
			t.Errorf("%s catches %q = %v, want %v", tc.f.Name, tc.body, got, tc.want)
		}
	}
}
