package telegram

import (
	"slices"
	"testing"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A folder holds the chats it names, and the chats of the kinds it takes that it does
// not name out (an archived one out too, where it says so); a chat it names it holds
// whatever its kinds. Whether chats are read or muted is not copied, and the folder
// says so.
func TestAFolderHoldsWhatItsFiltersSay(t *testing.T) {
	t.Parallel()
	ent := peer.NewEntities(
		map[int64]*tg.User{7: {ID: 7, FirstName: "Dana", Contact: true}, 8: {ID: 8, FirstName: "Sam"}, 9: {ID: 9, FirstName: "Bot", Bot: true}},
		map[int64]*tg.Chat{11: {ID: 11, Title: "Family"}},
		map[int64]*tg.Channel{21: {ID: 21, Title: "News", Broadcast: true}, 22: {ID: 22, Title: "Hikers", Megagroup: true}},
	)
	archived := &tg.Dialog{}
	archived.SetFolderID(archiveFolder)
	elems := []dialog{
		{peer: &tg.InputPeerUser{UserID: 7}, entities: ent},
		{peer: &tg.InputPeerUser{UserID: 8}, entities: ent},
		{peer: &tg.InputPeerUser{UserID: 9}, entities: ent},
		{peer: &tg.InputPeerChat{ChatID: 11}, entities: ent, info: archived},
		{peer: &tg.InputPeerChannel{ChannelID: 21}, entities: ent},
		{peer: &tg.InputPeerChannel{ChannelID: 22}, entities: ent},
	}
	kinds := chatKinds(42, elems)
	dana, sam, bot := roomID(42, 7), roomID(42, 8), roomID(42, 9)
	family, news, hikers := roomID(42, -11), roomID(42, -(channelMark+21)), roomID(42, -(channelMark+22))
	for name, c := range map[string]struct {
		f    *tg.DialogFilter
		want []domain.RoomID
		left int
	}{
		"contacts":     {&tg.DialogFilter{Contacts: true}, []domain.RoomID{dana}, 0},
		"non-contacts": {&tg.DialogFilter{NonContacts: true}, []domain.RoomID{sam}, 0},
		"groups":       {&tg.DialogFilter{Groups: true}, []domain.RoomID{family, hikers}, 0},
		"not archived": {&tg.DialogFilter{Groups: true, ExcludeArchived: true}, []domain.RoomID{hikers}, 0},
		"channels":     {&tg.DialogFilter{Broadcasts: true}, []domain.RoomID{news}, 0},
		"bots":         {&tg.DialogFilter{Bots: true}, []domain.RoomID{bot}, 0},
		"named out":    {&tg.DialogFilter{Groups: true, ExcludePeers: []tg.InputPeerClass{&tg.InputPeerChat{ChatID: 11}}}, []domain.RoomID{hikers}, 0},
		"named in":     {&tg.DialogFilter{IncludePeers: []tg.InputPeerClass{&tg.InputPeerUser{UserID: 8}, &tg.InputPeerChannel{ChannelID: 21}}}, []domain.RoomID{sam, news}, 0},
		"moods":        {&tg.DialogFilter{Contacts: true, ExcludeRead: true, ExcludeMuted: true}, []domain.RoomID{dana}, 2},
	} {
		g := folder(42, c.f, kinds)
		slices.Sort(c.want)
		if !slices.Equal(g.Rooms, c.want) || len(g.Left) != c.left {
			t.Errorf("%s: holds %v (left %v), want %v", name, g.Rooms, g.Left, c.want)
		}
	}
	if got := named(42, []tg.InputPeerClass{&tg.InputPeerUser{UserID: 99}, &tg.InputPeerUser{UserID: 7}}, kinds); !slices.Equal(got, []domain.RoomID{dana}) {
		t.Errorf("a named chat the account does not see was kept: %v", got)
	}
}
