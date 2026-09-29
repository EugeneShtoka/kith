package protoconv

// mapSlice converts a slice element by element; nil stays nil.
func mapSlice[T, U any](in []T, convert func(T) U) []U {
	if in == nil {
		return nil
	}
	out := make([]U, 0, len(in))
	for i := range in {
		out = append(out, convert(in[i]))
	}
	return out
}
