package config

import "testing"

// An account moved to another person takes its picture choice with it: the identity it
// left no longer names it, so the config still checks.
func TestAMovedAccountsPictureChoiceGoes(t *testing.T) {
	t.Parallel()
	display := MergeIdentity(Display{Identities: []Identity{
		{Alias: "Dana", IDs: []string{"telegram:1", "whatsapp:2@s.whatsapp.net"}, Picture: "telegram:1"},
		{Alias: "Eli", IDs: []string{"telegram:3"}, Picture: "telegram:3"},
	}}, "Eli", "telegram:1", "")
	for _, ident := range display.Identities {
		if ident.Alias == "Dana" && ident.Picture != "" {
			t.Errorf("Dana still names the picture of an account that left: %+v", ident)
		}
		if ident.Alias == "Eli" && ident.Picture != "telegram:3" {
			t.Errorf("Eli's own choice changed: %+v", ident)
		}
	}
}
