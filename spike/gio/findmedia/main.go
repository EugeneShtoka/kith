// Command findmedia lists cached video and audio messages, newest rooms first, for
// trying the spike's player on.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

func main() {
	path, err := config.Path()
	if err != nil {
		log.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		log.Fatal(err)
	}
	storage, err := setup.StorageFor(cfg, path)
	if err != nil {
		log.Fatal(err)
	}
	socket, err := daemon.SocketPath(storage)
	if err != nil {
		log.Fatal(err)
	}
	r := daemon.NewRemote(socket)
	ctx := context.Background()
	rooms, err := r.Rooms(ctx)
	if err != nil {
		log.Fatal(err)
	}
	found := 0
	for _, room := range rooms {
		msgs, err := r.CachedTimeline(ctx, room.ID)
		if err != nil {
			continue
		}
		for _, m := range msgs {
			if m.Media != nil && (m.Media.Type == domain.MediaVideo || m.Media.Type == domain.MediaAudio) {
				fmt.Printf("%s\t%s\t%s\t%s\t%d KB\t%s\n", m.Media.Type, room.ID, m.ID, m.Media.Mime, m.Media.Size>>10, room.DisplayName())
				found++
			}
		}
		if found >= 8 {
			return
		}
	}
}
