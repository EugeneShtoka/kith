package matrix

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// FetchEventSource fetches one event from the homeserver and returns its JSON
// as the homeserver served it, pretty-printed. Nothing is cached: a source read
// is a debugging aid, not a message to merge; the parsed form is what the rest
// of kith uses, which FetchEvent handles separately.
func (b *InProc) FetchEventSource(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]byte, error) {
	evt, err := b.client.GetEvent(ctx, b.ids.nativeRoom(roomID), b.ids.nativeEvent(eventID))
	if err != nil {
		return nil, fmt.Errorf("matrix: fetch event source %s: %w", eventID, err)
	}
	// MarshalIndent, not Marshal: the pager will show this, and a user reading
	// JSON by hand needs the whitespace to tell nesting from truncation.
	pretty, err := json.MarshalIndent(evt, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("matrix: format event source %s: %w", eventID, err)
	}
	return pretty, nil
}
