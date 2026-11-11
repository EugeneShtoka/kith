// A spike: a Gio desktop window over kithd, beside the Electron one, to measure
// startup and memory. A module of its own so the root module never builds Gio.
module github.com/EugeneShtoka/kith/spike/gio

go 1.26.3

replace github.com/EugeneShtoka/kith => ../..

replace github.com/slack-go/slack => github.com/beeper/slackgo v0.0.0-20260925001310-ea62f6cbc997

replace github.com/imroc/req/v3 => github.com/beeper/req/v3 v3.0.0-20260925135712-b49593750e56

require (
	gioui.org v0.10.3
	gioui.org/x v0.10.3
	github.com/EugeneShtoka/kith v0.0.0-00010101000000-000000000000
	golang.org/x/image v0.48.0
)

require (
	connectrpc.com/connect v1.21.1 // indirect
	filippo.io/age v1.3.2 // indirect
	filippo.io/hpke v0.4.0 // indirect
	gioui.org/shader v1.0.9 // indirect
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/adrg/xdg v0.5.3 // indirect
	github.com/coreos/go-systemd/v22 v22.7.0 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	github.com/godbus/dbus/v5 v5.2.4 // indirect
	github.com/gofrs/flock v0.13.1 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/rs/zerolog v1.35.1 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/exp v0.0.0-20261014143641-f0251eb4aa3e // indirect
	golang.org/x/exp/shiny v0.0.0-20250408133849-7e4ce0ab07d0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
