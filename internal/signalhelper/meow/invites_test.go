//go:build signal

package meow

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"
	"google.golang.org/protobuf/proto"
)

// An invite link as signalmeow writes it (as Signal's apps do) reads back as its
// group's key and password, and the key opens the name in the group's join info.
func TestAnInviteLinkOpensItsGroup(t *testing.T) {
	t.Parallel()

	key := libsignalgo.GroupMasterKey(bytes.Repeat([]byte{0x2a}, libsignalgo.GroupMasterKeyLength))
	password := []byte("the link's password")
	serialized := signalmeow.InviteLinkPasswordFromBytes(password)
	link, err := (&signalmeow.Group{
		GroupMasterKey:     types.SerializedGroupMasterKey(base64.StdEncoding.EncodeToString(key[:])),
		InviteLinkPassword: &serialized,
	}).GetInviteLink()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimPrefix(link, "https://signal.group/#"), "="))
	if err != nil {
		t.Fatal(err)
	}
	in, err := readInvite(raw)
	if err != nil || in.key != key || !bytes.Equal(in.password, password) {
		t.Fatalf("readInvite(%s) = (%x, %q, %v), want the key and password written", link, in.key, in.password, err)
	}

	blob, _ := proto.Marshal(&signalpb.GroupAttributeBlob{Content: &signalpb.GroupAttributeBlob_Title{Title: " Book club "}})
	sealed, err := in.secret.EncryptBlobWithPaddingDeterministic(libsignalgo.GenerateRandomness(), blob, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := in.title(&signalpb.GroupJoinInfo{Title: sealed}); err != nil || got != "Book club" {
		t.Errorf("title = (%q, %v), want Book club", got, err)
	}
	other, _ := readInvite(mustInvite(t, bytes.Repeat([]byte{0x2b}, libsignalgo.GroupMasterKeyLength), password))
	if got, err := other.title(&signalpb.GroupJoinInfo{Title: sealed}); err == nil {
		t.Errorf("another group's key opened %q", got)
	}
}

// An invite without a whole key or a password names no group.
func TestAnInviteWithoutKeyOrPasswordIsRefused(t *testing.T) {
	t.Parallel()

	key := bytes.Repeat([]byte{0x2a}, libsignalgo.GroupMasterKeyLength)
	for name, raw := range map[string][]byte{
		"short key":   mustInvite(t, key[:31], []byte("pw")),
		"no password": mustInvite(t, key, nil),
		"not one":     {0xff, 0xff},
	} {
		if _, err := readInvite(raw); err == nil {
			t.Errorf("%s: readInvite took it", name)
		}
	}
}

// The server's public params, kept here as signalmeow keeps them, are whole.
func TestTheServerParamsAreWhole(t *testing.T) {
	t.Parallel()

	if _, err := serverParams(); err != nil {
		t.Fatal(err)
	}
}

// Each refusal of the groups service says what went wrong.
func TestTheGroupsServiceRefusalsSayWhy(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		answer answer
		want   error
	}{
		{answer{status: http.StatusOK}, nil},
		{answer{status: http.StatusForbidden, reason: "banned"}, errBanned},
		{answer{status: http.StatusForbidden}, errInviteGone},
		{answer{status: http.StatusNotFound}, errNoGroup},
		{answer{status: http.StatusLocked}, errGroupEnded},
		{answer{status: http.StatusConflict}, errGroupBusy},
	} {
		if got := c.answer.refusal(); !errors.Is(got, c.want) {
			t.Errorf("refusal of %+v = %v, want %v", c.answer, got, c.want)
		}
	}
	if got := (answer{status: http.StatusTooManyRequests}).refusal(); got == nil || !strings.Contains(got.Error(), "429") {
		t.Errorf("refusal of a 429 = %v, want it named", got)
	}
}

func mustInvite(t *testing.T, key, password []byte) []byte {
	t.Helper()
	raw, err := proto.Marshal(&signalpb.GroupInviteLink{Contents: &signalpb.GroupInviteLink_ContentsV1{
		ContentsV1: &signalpb.GroupInviteLink_GroupInviteLinkContentsV1{GroupMasterKey: key, InviteLinkPassword: password},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
