package domain

import (
	"testing"
	"time"
)

// A placeholder replaced by an edit bringing a picture shows the picture once merged;
// a newer edit's picture replaces an older one's.
func TestAnEditsPictureMergesIn(t *testing.T) {
	t.Parallel()
	stand := Message{ID: "$s", Body: "Decrypting message from WhatsApp failed", Placeholder: true, Timestamp: time.UnixMilli(1000)}
	first := Message{ID: "$s", Body: "look", Edited: true, EditedAt: time.UnixMilli(2000), RevisionID: "$e1",
		Media: &Media{Type: MediaImage, Name: "one.jpg"}}
	second := first
	second.EditedAt, second.RevisionID, second.Media = time.UnixMilli(3000), "$e2", &Media{Type: MediaImage, Name: "two.jpg"}
	got := MergeMessages([]Message{stand}, []Message{first, second})
	if len(got) != 1 || got[0].Media == nil || got[0].Media.Name != "two.jpg" || got[0].Body != "look" {
		t.Errorf("merged = %+v (media %+v)", got, got[0].Media)
	}
}
