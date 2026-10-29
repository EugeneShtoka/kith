package daemon

import (
	"reflect"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/notify"
)

// A notification crosses the wire whole: every field a listener shows it by, the rules
// it picks a sound by among them, in their order.
func TestANoticeCrossesTheWireWhole(t *testing.T) {
	t.Parallel()
	alert := notify.Notification{
		Sender: "Alice", SenderID: "matrix:@alice:x", Room: "Standup", Space: "Work", Body: "hi",
		Protocol: "Matrix", Sent: time.UnixMilli(1_700_000_000_123), Date: "2023-11-14", Time: "22:13",
		Rules: []string{"the boss", "work", "all rooms"}, Silent: true, Replaces: "room",
	}
	got := protoToNotice(noticeToProto(notice{id: "n1", alert: alert, code: "1234"}))
	want := Notice{ID: "n1", Alert: alert, Code: "1234"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v\nwant %+v", got, want)
	}
}
