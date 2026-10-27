package domain

import "context"

// Profile is a named setup a client runs: the rooms it shows, and the people it hides.
// A room outside Rooms shows nothing and counts for nothing in it.
type Profile struct {
	Name   string
	Rooms  PlaceFilter
	People PeopleFilter
}

// PeopleFilter is whom a profile hides: their messages, and the direct replies to
// them, go as a muted tag's people's do everywhere. IDs are person IDs as written
// ([[tag]] people's form); Fields are phonebook card terms, key:value as written.
type PeopleFilter struct {
	IDs    []string
	Fields []string
}

// Empty reports whether the filter hides nobody.
func (f PeopleFilter) Empty() bool { return len(f.IDs) == 0 && len(f.Fields) == 0 }

// profileKey carries the profile a request runs in.
type profileKey struct{}

// WithProfile is ctx running in the [[profile]] named; "" is none, which reads as the
// people everyone hides alone.
func WithProfile(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, profileKey{}, name)
}

// ProfileIn is the profile ctx runs in, as WithProfile set it; "" when none.
func ProfileIn(ctx context.Context) string {
	name, _ := ctx.Value(profileKey{}).(string)
	return name
}
