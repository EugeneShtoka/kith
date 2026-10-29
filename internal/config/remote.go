package config

// Remote is [remote]: kithd's TLS listener for clients on other machines. Listen is
// each host:port to listen on; empty listens nowhere.
type Remote struct {
	Listen []string `toml:"listen"`
}
