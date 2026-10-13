package main

import (
	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/matrix"
	"github.com/EugeneShtoka/kith/internal/meta"
	"github.com/EugeneShtoka/kith/internal/route"
	"github.com/EugeneShtoka/kith/internal/slack"
	"github.com/EugeneShtoka/kith/internal/telegram"
	"github.com/EugeneShtoka/kith/internal/whatsapp"
)

// Compile-time capability assertions per network: the router asks for capabilities by
// interface, so a drifted method signature would silently make the network refuse the
// call. Add a capability here when a network gains it.
var (
	_ interface {
		route.Adapter
		route.Session
		route.Resync
		route.RoomLister
		route.Homes
		route.ReadState
		route.Stars
		route.TagMoves
		route.History
		route.Sender
		route.Typist
		route.Uploader
		route.Reactor
		route.Redactor
		route.People
		route.Media
		route.Encryption
		route.SpaceLister
		route.SpaceEditor
		route.Threads
		api.Membership
		api.Verification
		api.Keys
	} = (*matrix.Adapter)(nil)

	_ interface {
		route.Adapter
		route.RoomLister
		route.Homes
		route.ReadState
		route.Archiver
		route.Stars
		route.TagMoves
		route.History
		route.Sender
		route.Typist
		route.Uploader
		route.Reactor
		route.Voter
		route.Redactor
		route.People
		route.Media
		route.Encryption
		route.SpaceLister
		route.Leaver
		route.SignOuter
		configChecker
	} = (*whatsapp.Adapter)(nil)

	_ interface {
		route.Adapter
		route.TagMoves
		route.RoomLister
		route.Homes
		route.ReadState
		route.History
		route.Sender
		route.Typist
		route.Uploader
		route.Reactor
		route.Redactor
		route.People
		route.Media
		route.Encryption
		route.SpaceLister
		route.Threads
		route.SignOuter
		configChecker
	} = (*slack.Adapter)(nil)

	_ interface {
		route.Adapter
		route.TagMoves
		route.RoomLister
		route.Homes
		route.ReadState
		route.Archiver
		route.Groupings
		route.History
		route.Sender
		route.Typist
		route.Uploader
		route.Reactor
		route.Redactor
		route.People
		route.Media
		route.Encryption
		route.SpaceLister
		route.Leaver
		route.ChatDeleter
		route.SignOuter
		route.Voter
		configChecker
		roomsRewriter
		cacheWriter
	} = (*telegram.Adapter)(nil)

	// Messenger, so far: login and connection (rooms and messages come next).
	_ interface {
		route.Adapter
		configChecker
	} = (*meta.Adapter)(nil)
)
