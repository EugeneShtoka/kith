package domain

import "testing"

// A suggested name is the country, else prefix plus last digits, avoiding taken names.
func TestPhoneNames(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ got, want string }{
		{PhoneName("447700900123", "wa", nil), "gb"},
		{PhoneName("12025550147", "wa", []string{"gb"}), "us"},
		{PhoneName("4915200000000", "wa", nil), "de"},
		{PhoneName("79161234567", "wa", nil), "ru"},
		{PhoneName("447700900000", "wa", []string{"gb", "gb2"}), "gb3"},
		{PhoneName("8881234567", "tg", nil), "tg4567"}, // +888 is no country's
		{FreeName("work", []string{"work"}), "work2"},
	} {
		if c.got != c.want {
			t.Errorf("suggested %q, want %q", c.got, c.want)
		}
	}
	if PhoneDigits("+44 7700-900 123") != "447700900123" || CheckPhone("123") == nil || CheckPhone("447700900123") != nil {
		t.Error("a number's digits, or its check, is off")
	}
}

// No calling code is a prefix of another.
func TestCallingCodesArePrefixFree(t *testing.T) {
	t.Parallel()
	for code := range callingCodes {
		for other := range callingCodes {
			if code != other && len(other) > len(code) && other[:len(code)] == code {
				t.Errorf("+%s is the start of +%s", code, other)
			}
		}
	}
}
