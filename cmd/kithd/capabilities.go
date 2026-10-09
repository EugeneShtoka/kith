package main

import (
	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/matrix"
	"github.com/EugeneShtoka/kith/internal/route"
	"github.com/EugeneShtoka/kith/internal/slack"
	"github.com/EugeneShtoka/kith/internal/telegram"
	"github.com/EugeneShtoka/kith/internal/whatsapp"
)

// What each network can do, said where it is wired. The router asks an adapter for a
// capability when a call needs it, so a method whose signature drifted would silently
// make the network refuse that call; these make it a build error instead. A
// capability a network gains is added here.
var (
	_ interface {
		route.Adapter
		route.Session
		route.Resync
		route.RoomLister
		route.Homes
		route.ReadState
		route.Stars
		route.SpamReports
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
		route.SpamReports
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
)
