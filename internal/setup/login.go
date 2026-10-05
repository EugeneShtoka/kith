package setup

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// What signing in to a network says and checks, shared by `kith login …` and the
// in-app :login, so the two say the same.

// WhatsAppLinkSteps is what to do on the phone with a pairing code.
const WhatsAppLinkSteps = "On the phone: WhatsApp → Settings → Linked devices → Link a device → " +
	"“Link with phone number instead”, and type the code."

// SlackSessionHelp is where a workspace's session is copied from: Open, the page to
// open in a browser signed in to it; Token and Cookie, how to find each half there.
// workspace is its address ("acme") or its team ID ("T0123456789").
type SlackSessionHelp struct {
	Where, Open, Token, Cookie string
}

// SlackSession is the help for workspace.
func SlackSession(workspace string) SlackSessionHelp {
	h := SlackSessionHelp{
		Where: workspace + ".slack.com", Open: "https://" + workspace + ".slack.com",
		Token: `Object.values(JSON.parse(localStorage.localConfig_v2).teams).find(t => t.url.includes("//` + workspace + `.")).token`,
	}
	if IsSlackTeamID(workspace) {
		h.Where, h.Open = "workspace "+workspace, "https://app.slack.com/client/"+workspace
		h.Token = `JSON.parse(localStorage.localConfig_v2).teams["` + workspace + `"].token`
	}
	h.Cookie = "Application (Storage in Firefox) → Cookies → https://app.slack.com — the value of the cookie named d"
	return h
}

// SlackSessionWarning is what the copied session is, said before it is asked for.
const SlackSessionWarning = "Both are a session: whoever has them can read and write as you. kith keeps them in " +
	"the system keyring. Signing out of Slack in that browser ends this session too."

// IsSlackTeamID reports whether a workspace is named by its team ID ("T0123456789")
// rather than its address, which SlackWorkspace lower-cases.
func IsSlackTeamID(workspace string) bool { return slackTeamID.MatchString(workspace) }

// CheckSlackToken catches a token that is not one (the cookie pasted instead, say)
// before Slack is asked.
func CheckSlackToken(token string) error {
	if !strings.HasPrefix(token, "xoxc-") {
		return errors.New("the token starts with xoxc- — copy it from the console line")
	}
	return nil
}

// CheckSlackCookie catches a cookie that is not the d one before Slack is asked.
func CheckSlackCookie(cookie string) error {
	if !strings.HasPrefix(cookie, "xoxd-") {
		return errors.New("the cookie d starts with xoxd- — copy its value from the cookies of https://app.slack.com")
	}
	return nil
}

// CheckSlackSession catches the two halves swapped or mistyped before Slack is asked.
func CheckSlackSession(token, cookie string) error {
	if err := CheckSlackToken(token); err != nil {
		return err
	}
	return CheckSlackCookie(cookie)
}

// CheckSlackWorkspace refuses what is no workspace address, ID or link.
func CheckSlackWorkspace(workspace string) error {
	if !workspaceName.MatchString(workspace) && !slackTeamID.MatchString(workspace) {
		return fmt.Errorf("%q is not a Slack workspace — write its address (acme, for acme.slack.com), "+
			"or paste a link from the web client (app.slack.com/client/T…)", workspace)
	}
	return nil
}

// CheckPhone refuses what is no international number: 7 to 15 digits.
func CheckPhone(digits string) error {
	if len(digits) < 7 || len(digits) > 15 {
		return errors.New("write the number with its country code, as +359 88 123 4567")
	}
	return nil
}

// FreeName is base, or base with the first number that makes it unused among taken.
func FreeName(base string, taken []string) string {
	if !slices.Contains(taken, base) {
		return base
	}
	for i := 2; ; i++ {
		if name := fmt.Sprintf("%s%d", base, i); !slices.Contains(taken, name) {
			return name
		}
	}
}

// WhatsAppName suggests a name for the account with these digits: its country's code
// ("bg" for +359), else "wa" and its last four digits.
func WhatsAppName(digits string, taken []string) string {
	if country := PhoneCountry(digits); country != "" {
		return FreeName(country, taken)
	}
	return FreeName("wa"+digits[max(0, len(digits)-4):], taken)
}

// SlackName suggests a name for a workspace: its address, or "work" for one named by
// its ID, which says nothing.
func SlackName(workspace string, taken []string) string {
	if IsSlackTeamID(workspace) || workspace == "" {
		return FreeName("work", taken)
	}
	return FreeName(workspace, taken)
}

// PhoneCountry is the country an international number's calling code names, as its
// two-letter code; "" for a code it does not know. A code several countries share
// (+1, +7) names the largest of them.
func PhoneCountry(digits string) string {
	for n := 3; n >= 1; n-- {
		if len(digits) > n {
			if country, ok := callingCodes[digits[:n]]; ok {
				return country
			}
		}
	}
	return ""
}

// callingCodes is each ITU calling code's country.
var callingCodes = map[string]string{
	"1": "us", "7": "ru", "20": "eg", "27": "za", "30": "gr", "31": "nl", "32": "be", "33": "fr",
	"34": "es", "36": "hu", "39": "it", "40": "ro", "41": "ch", "43": "at", "44": "gb", "45": "dk",
	"46": "se", "47": "no", "48": "pl", "49": "de", "51": "pe", "52": "mx", "53": "cu", "54": "ar",
	"55": "br", "56": "cl", "57": "co", "58": "ve", "60": "my", "61": "au", "62": "id", "63": "ph",
	"64": "nz", "65": "sg", "66": "th", "81": "jp", "82": "kr", "84": "vn", "86": "cn", "90": "tr",
	"91": "in", "92": "pk", "93": "af", "94": "lk", "95": "mm", "98": "ir",
	"211": "ss", "212": "ma", "213": "dz", "216": "tn", "218": "ly", "220": "gm", "221": "sn",
	"222": "mr", "223": "ml", "224": "gn", "225": "ci", "226": "bf", "227": "ne", "228": "tg",
	"229": "bj", "230": "mu", "231": "lr", "232": "sl", "233": "gh", "234": "ng", "235": "td",
	"236": "cf", "237": "cm", "238": "cv", "239": "st", "240": "gq", "241": "ga", "242": "cg",
	"243": "cd", "244": "ao", "245": "gw", "248": "sc", "249": "sd", "250": "rw", "251": "et",
	"252": "so", "253": "dj", "254": "ke", "255": "tz", "256": "ug", "257": "bi", "258": "mz",
	"260": "zm", "261": "mg", "262": "re", "263": "zw", "264": "na", "265": "mw", "266": "ls",
	"267": "bw", "268": "sz", "269": "km", "290": "sh", "291": "er", "297": "aw", "298": "fo",
	"299": "gl", "350": "gi", "351": "pt", "352": "lu", "353": "ie", "354": "is", "355": "al",
	"356": "mt", "357": "cy", "358": "fi", "359": "bg", "370": "lt", "371": "lv", "372": "ee",
	"373": "md", "374": "am", "375": "by", "376": "ad", "377": "mc", "378": "sm", "380": "ua",
	"381": "rs", "382": "me", "383": "xk", "385": "hr", "386": "si", "387": "ba", "389": "mk",
	"420": "cz", "421": "sk", "423": "li", "500": "fk", "501": "bz", "502": "gt", "503": "sv",
	"504": "hn", "505": "ni", "506": "cr", "507": "pa", "509": "ht", "590": "gp", "591": "bo",
	"592": "gy", "593": "ec", "595": "py", "597": "sr", "598": "uy", "670": "tl", "673": "bn",
	"674": "nr", "675": "pg", "676": "to", "677": "sb", "678": "vu", "679": "fj", "680": "pw",
	"685": "ws", "686": "ki", "687": "nc", "689": "pf", "691": "fm", "692": "mh", "850": "kp",
	"852": "hk", "853": "mo", "855": "kh", "856": "la", "880": "bd", "886": "tw", "960": "mv",
	"961": "lb", "962": "jo", "963": "sy", "964": "iq", "965": "kw", "966": "sa", "967": "ye",
	"968": "om", "970": "ps", "971": "ae", "972": "il", "973": "bh", "974": "qa", "975": "bt",
	"976": "mn", "977": "np", "992": "tj", "993": "tm", "994": "az", "995": "ge", "996": "kg",
	"998": "uz",
}
