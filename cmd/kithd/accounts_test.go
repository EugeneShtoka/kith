package main

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The default is marked by the network's own config, and a reload moves it.
func TestEachNetworksDefaultIsMarked(t *testing.T) {
	t.Parallel()
	accounts := func() []domain.Account {
		return []domain.Account{
			{Network: domain.ProtocolTelegram, Name: "home"}, {Network: domain.ProtocolTelegram, Name: "work"},
			{Network: domain.ProtocolWhatsApp, Name: "work"},
		}
	}
	d := &accountDefaults{}
	d.use(config.Config{Telegram: config.Telegram{Default: "work"}})
	got := d.mark(accounts())
	if got[0].Default || !got[1].Default || got[2].Default {
		t.Errorf("marked %+v; want only Telegram's work", got)
	}
	d.use(config.Config{})
	for _, a := range d.mark(accounts()) {
		if a.Default {
			t.Errorf("%+v is still the default after the config dropped it", a)
		}
	}
}
