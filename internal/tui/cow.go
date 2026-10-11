package tui

import "maps"

// Model is copied by value on every Update and maps are references, so writing one in
// place would change every copy. Model's maps are therefore copy-on-write (these
// helpers), and state shared on purpose lives behind a pointer (derivedCache). Nothing
// may hold a map across Updates expecting later writes; tagged.go memoizes on these
// maps' identity. TestNoValueReceiverWritesAMap enforces this.

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
