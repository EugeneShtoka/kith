package tui

import "maps"

// Model is copied by value on every Update, and a map is a reference: writing one in
// place would change every copy, including ones a handler returns unchanged or a test
// keeps. So Model's maps are copy-on-write (these helpers), and state shared on
// purpose lives behind a pointer (derivedCache). Nothing may hold a map across
// Updates expecting to see later writes: rail groups are judged by the view they are
// handed (group.admits), and what the tags make of each room is memoized by these
// maps' identity (tagged.go). TestNoValueReceiverWritesAMap holds the rule.

// withEntry returns a copy of src with k set to v.
func withEntry[K comparable, V any](src map[K]V, k K, v V) map[K]V {
	out := make(map[K]V, len(src)+1)
	maps.Copy(out, src)
	out[k] = v
	return out
}

// withoutEntry returns a copy of src without k, or src itself when k is absent.
func withoutEntry[K comparable, V any](src map[K]V, k K) map[K]V {
	if _, ok := src[k]; !ok {
		return src
	}
	out := maps.Clone(src)
	delete(out, k)
	return out
}
