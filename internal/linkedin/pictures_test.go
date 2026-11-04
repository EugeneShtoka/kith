package linkedin

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"go.mau.fi/mautrix-linkedin/pkg/linkedingo"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func withPicture(p linkedingo.MessagingParticipant, img *linkedingo.VectorImage) linkedingo.MessagingParticipant {
	switch {
	case p.ParticipantType.Member != nil:
		m := *p.ParticipantType.Member
		m.ProfilePicture = img
		p.ParticipantType.Member = &m
	case p.ParticipantType.Organization != nil:
		o := *p.ParticipantType.Organization
		o.Logo = img
		p.ParticipantType.Organization = &o
	}
	return p
}

// The artifact asked for is the smallest at least pictureWidth wide, else the largest.
func TestAPictureIsTheSmallestArtifactWideEnough(t *testing.T) {
	t.Parallel()
	sized := func(widths ...int) *linkedingo.VectorImage {
		img := &linkedingo.VectorImage{RootURL: "https://media.example/p/"}
		for _, w := range widths {
			img.Artifacts = append(img.Artifacts, linkedingo.VectorArtifact{FileIdentifyingURLPathSegment: strings.Repeat("w", w/100+1), Width: w})
		}
		return img
	}
	for _, tc := range []struct {
		img  *linkedingo.VectorImage
		want picture
	}{
		{sized(800, 100, 200), picture{"https://media.example/p/", "https://media.example/p/ww"}},
		{sized(50, 80), picture{"https://media.example/p/", "https://media.example/p/w"}},
		{sized(), picture{}},
		{nil, picture{}},
	} {
		if got := pictureOf(withPicture(people[dana], tc.img)); got != tc.want {
			t.Errorf("pictureOf(%+v) = %+v, want %+v", tc.img, got, tc.want)
		}
	}
}

// A one-to-one conversation shows its person's picture (an organization's logo), a
// group none; each is downloaded with the session once per picture, and again only
// when it changes.
func TestAConversationShowsItsPersonsPicture(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var fetched []string
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetched = append(fetched, r.URL.Path)
		mu.Unlock()
		_, _ = w.Write([]byte("picture " + r.URL.Path))
	}))
	t.Cleanup(cdn.Close)
	img := func(root string) *linkedingo.VectorImage {
		return &linkedingo.VectorImage{RootURL: cdn.URL + "/" + root + "/", Artifacts: []linkedingo.VectorArtifact{{FileIdentifyingURLPathSegment: "s", Width: 200}}}
	}

	a, c, cache, _ := cachedAdapter(t)
	ctx := context.Background()
	acmeDM := "2-YWNtZS9tZQ=="
	// with is a conversation whose participant other has picture root.
	with := func(c linkedingo.Conversation, other, root string) linkedingo.Conversation {
		c.ConversationParticipants = slices.Clone(c.ConversationParticipants)
		for i, p := range c.ConversationParticipants {
			if profileOf(p.EntityURN) == other {
				c.ConversationParticipants[i] = withPicture(p, img(root))
			}
		}
		return c
	}
	a.takeConversations(ctx, c, []linkedingo.Conversation{
		with(conversation(danaDM, "", 1000, 0, true, me, dana), dana, "dana1"),
		with(conversation(acmeDM, "", 1000, 0, true, me, acmeOrg), acmeOrg, "acme1"),
		with(conversation(group, "Board", 1000, 0, true, me, dana, eli), dana, "dana1"),
	}, nil)
	client := a.clientFor(ctx, aSession(me, "x"), linkedingo.Handlers{})
	pictures := func() map[domain.RoomID]string {
		out := map[domain.RoomID]string{}
		rooms, err := cache.Rooms(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for i := range rooms {
			data, err := cache.Picture(ctx, rooms[i].ID)
			if err != nil {
				t.Fatal(err)
			}
			out[rooms[i].ID] = strings.TrimPrefix(rooms[i].Picture, cdn.URL) + "=" + string(data)
		}
		return out
	}

	if err := a.sweepPictures(ctx, client, c.inbox.conversationPictures(c.profile)); err != nil {
		t.Fatal(err)
	}
	want := map[domain.RoomID]string{
		roomID(me, danaDM): "/dana1/=picture /dana1/s", roomID(me, acmeDM): "/acme1/=picture /acme1/s", roomID(me, group): "=",
	}
	if got := pictures(); !maps.Equal(got, want) {
		t.Fatalf("pictures = %v, want %v", got, want)
	}

	// Listed again: Dana's picture changed, Acme's did not.
	mu.Lock()
	fetched = nil
	mu.Unlock()
	a.takeConversations(ctx, c, []linkedingo.Conversation{
		with(conversation(danaDM, "", 2000, 0, true, me, dana), dana, "dana2"),
		with(conversation(acmeDM, "", 2000, 0, true, me, acmeOrg), acmeOrg, "acme1"),
	}, nil)
	if err := a.sweepPictures(ctx, client, c.inbox.conversationPictures(c.profile)); err != nil {
		t.Fatal(err)
	}
	want[roomID(me, danaDM)] = "/dana2/=picture /dana2/s"
	if got := pictures(); !maps.Equal(got, want) {
		t.Fatalf("after Dana's changed, pictures = %v, want %v", got, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(fetched, []string{"/dana2/s"}) {
		t.Errorf("the second sweep fetched %v, want only Dana's new picture", fetched)
	}
}
