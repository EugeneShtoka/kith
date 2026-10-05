package telegram

import (
	"errors"
	"strconv"

	"github.com/EugeneShtoka/kith/internal/api"
)

// App is a Telegram app as my.telegram.org registers one: every client identifies as
// one, by its api_id and api_hash.
type App struct {
	ID   int    `json:"api_id"`
	Hash string `json:"api_hash"`
}

// Valid reports whether both parts are there.
func (a App) Valid() bool { return a.ID > 0 && a.Hash != "" }

// builtinID and builtinHash are kith's own app, set when the daemon is built
// (-ldflags "-X …/internal/telegram.builtinID=… -X …builtinHash=…", from
// TELEGRAM_API_ID and TELEGRAM_API_HASH). A build without them has none: they are
// not in the repository.
var builtinID, builtinHash string

// BuiltIn is kith's own app; ok false when this build carries none.
func BuiltIn() (App, bool) {
	id, err := strconv.Atoi(builtinID)
	app := App{ID: id, Hash: builtinHash}
	return app, err == nil && app.Valid()
}

var (
	// errHalfApp is an api_id without its hash, or a hash without its ID.
	errHalfApp = errors.New("telegram: an app is its api_id and its api_hash together; give both, or neither for kith's own")
	// errNoBuiltIn is a login with no app, in a build that carries none of its own.
	errNoBuiltIn = errors.New("telegram: this kithd was built without an app of its own; give your app's api_id " +
		"and api_hash (my.telegram.org → API development tools)")
)

// appFor is the app a login goes through: the one given, else kith's own.
func appFor(given api.TelegramApp) (App, error) {
	app := App{ID: given.ID, Hash: given.Hash}
	switch {
	case app.Valid():
		return app, nil
	case given.ID != 0 || given.Hash != "":
		return App{}, errHalfApp
	}
	if builtin, ok := BuiltIn(); ok {
		return builtin, nil
	}
	return App{}, errNoBuiltIn
}
