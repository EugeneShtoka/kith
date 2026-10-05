package telegram

// App is a Telegram app as my.telegram.org registers one: every client identifies as
// one, by its api_id and api_hash.
type App struct {
	ID   int    `json:"api_id"`
	Hash string `json:"api_hash"`
}

// Valid reports whether both parts are there.
func (a App) Valid() bool { return a.ID > 0 && a.Hash != "" }
