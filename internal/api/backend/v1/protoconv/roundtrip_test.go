package protoconv_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// TestEveryFieldSurvivesTheRoundTrip fills every field by reflection, so a field
// added to a domain type is covered the moment it exists (a missing `mentioned`
// once shipped this way).
func TestEveryFieldSurvivesTheRoundTrip(t *testing.T) {
	t.Parallel()

	// Fields that deliberately do not cross the wire, matched by name at any depth.
	offWire := map[string]string{
		"RevisionID": "only the daemon, which writes the cache, needs it",
	}

	for _, tc := range []struct {
		name  string
		value any
		trip  func(any) any
	}{
		{"Message", domain.Message{}, func(v any) any {
			return protoconv.ProtoToMessage(protoconv.MessageToProto(v.(domain.Message)))
		}},
		{"Room", domain.Room{}, func(v any) any {
			return protoconv.ProtoToRoom(protoconv.RoomToProto(v.(domain.Room)))
		}},
		{"Space", domain.Space{}, func(v any) any {
			return protoconv.ProtoToSpace(protoconv.SpaceToProto(v.(domain.Space)))
		}},
		{"Unread", domain.Unread{}, func(v any) any {
			return protoconv.ProtoToUnread(protoconv.UnreadToProto(v.(domain.Unread)))
		}},
		{"Reaction", domain.Reaction{}, func(v any) any {
			return protoconv.ProtoToReaction(protoconv.ReactionToProto(v.(domain.Reaction)))
		}},
		{"SearchHit", domain.SearchHit{}, func(v any) any {
			return protoconv.ProtoToSearchHit(protoconv.SearchHitToProto(v.(domain.SearchHit)))
		}},
		{"SearchRequest", domain.SearchRequest{}, func(v any) any {
			return protoconv.ProtoToSearchRequest(protoconv.SearchRequestToProto(v.(domain.SearchRequest)))
		}},
		{"Draft", domain.Draft{}, func(v any) any {
			return protoconv.ProtoToDraft(protoconv.DraftToProto(v.(domain.Draft)))
		}},
		{"Activity", domain.Activity{}, func(v any) any {
			return protoconv.ProtoToActivity(protoconv.ActivityToProto(v.(domain.Activity)))
		}},
		{"ReactionUpdate", domain.ReactionUpdate{}, func(v any) any {
			return protoconv.ProtoToReactionUpdate(protoconv.ReactionUpdateToProto(v.(domain.ReactionUpdate)))
		}},
		{"ScheduledMessage", domain.ScheduledMessage{}, func(v any) any {
			pb := protoconv.ScheduledToProto([]domain.ScheduledMessage{v.(domain.ScheduledMessage)})
			return protoconv.ProtoToScheduled(pb[0])
		}},
		{"Verification", domain.Verification{}, func(v any) any {
			got, ok := protoconv.ProtoToVerification(protoconv.VerificationToProto(v.(domain.Verification)))
			if !ok {
				return nil
			}
			return got
		}},
		{"TimelinePage", domain.TimelinePage{}, func(v any) any {
			return protoconv.ProtoToTimelinePage(protoconv.TimelinePageToProto(v.(domain.TimelinePage)))
		}},
		{"StoredDraft", domain.StoredDraft{}, func(v any) any {
			return protoconv.ProtoToStoredDraft(protoconv.StoredDraftToProto(v.(domain.StoredDraft)))
		}},
		{"CompleteRequest", domain.CompleteRequest{}, func(v any) any {
			return protoconv.ProtoToCompleteRequest(protoconv.CompleteRequestToProto(v.(domain.CompleteRequest)))
		}},
		{"ModelRequest", domain.ModelRequest{}, func(v any) any {
			return protoconv.ProtoToModelRequest(protoconv.ModelRequestToProto(v.(domain.ModelRequest)))
		}},
		{"ModelResult", domain.ModelResult{}, func(v any) any {
			return protoconv.ProtoToModelResult(protoconv.ModelResultToProto(v.(domain.ModelResult)))
		}},
		{"SpamVerdict", domain.SpamVerdict{}, func(v any) any {
			return protoconv.ProtoToSpam(protoconv.SpamToProto([]domain.SpamVerdict{v.(domain.SpamVerdict)})[0])
		}},
		{"Deletion", domain.Deletion{}, func(v any) any {
			return protoconv.ProtoToDeletion(protoconv.DeletionToProto(v.(domain.Deletion)))
		}},
		{"Thread", domain.Thread{}, func(v any) any {
			return protoconv.ProtoToThreads(protoconv.ThreadsToProto([]domain.Thread{v.(domain.Thread)}))[0]
		}},
		{"Revision", domain.Revision{}, func(v any) any {
			return protoconv.ProtoToRevisions(protoconv.RevisionsToProto([]domain.Revision{v.(domain.Revision)}))[0]
		}},
		{"Member", domain.Member{}, func(v any) any {
			return protoconv.ProtoToMembers(protoconv.MembersToProto([]domain.Member{v.(domain.Member)}))[0]
		}},
		{"WordCandidate", domain.WordCandidate{}, func(v any) any {
			return protoconv.ProtoToWordCandidates(protoconv.WordCandidatesToProto([]domain.WordCandidate{v.(domain.WordCandidate)}))[0]
		}},
		{"KeyBackup", domain.KeyBackup{}, func(v any) any {
			return protoconv.ProtoToKeyBackup(protoconv.KeyBackupToProto(v.(domain.KeyBackup)))
		}},
		{"SpellSuggestion", domain.SpellSuggestion{}, func(v any) any {
			return protoconv.ProtoToSpellSuggestion(protoconv.SpellSuggestionToProto(v.(domain.SpellSuggestion)))
		}},
		{"ModelSuggestion", domain.ModelSuggestion{}, func(v any) any {
			return protoconv.ProtoToModelSuggestion(protoconv.ModelSuggestionToProto(v.(domain.ModelSuggestion)))
		}},
		{"Misspelling", domain.Misspelling{}, func(v any) any {
			return protoconv.ProtoToMisspellings(protoconv.MisspellingsToProto([]domain.Misspelling{v.(domain.Misspelling)}))[0]
		}},
		{"ReactionRefusal", domain.ReactionRefusal{}, func(v any) any {
			return protoconv.ProtoToReactionRefusals(protoconv.ReactionRefusalsToProto([]domain.ReactionRefusal{v.(domain.ReactionRefusal)}))[0]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			filled := reflect.New(reflect.TypeOf(tc.value)).Elem()
			fill(t, filled, tc.name)
			zeroNamed(filled, offWire)
			want := filled.Interface()

			got := tc.trip(want)
			// Compared as instants: location is a per-subsystem convention.
			got, want = inUTC(got), inUTC(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("a field did not survive the wire.\n got %+v\nwant %+v", got, want)
				reportFirstDifference(t, reflect.ValueOf(got), reflect.ValueOf(want), tc.name)
			}
		})
	}
}

// Formatting read from markup reaches the client drawing exactly what the daemon drew
// from it; only the markup stays behind.
func TestFormattingCrossesTheWireAsItDraws(t *testing.T) {
	t.Parallel()

	for _, markup := range []string{
		"<b>bold</b> and <i>it</i>",
		`see <a href="https://example.org/x">the link</a>`,
		`<span data-mx-spoiler="plot">he dies</span>`,
		"<blockquote>quoted</blockquote><h2>Heading</h2><ul><li>one</li><li>two</li></ul>",
		"<pre><code>code</code></pre> <del>gone</del> <u>under</u>",
	} {
		sent := richtext.FromMarkup(markup)
		got := protoconv.ProtoToFormatted(protoconv.FormattedToProto(sent))
		if got.Text() != sent.Text() || !reflect.DeepEqual(got.Spans(), sent.Spans()) {
			t.Errorf("%s:\n got %q %+v\nwant %q %+v", markup, got.Text(), got.Spans(), sent.Text(), sent.Spans())
		}
		if got.Markup() != "" {
			t.Errorf("%s: markup crossed the wire: %q", markup, got.Markup())
		}
	}
	if pb := protoconv.FormattedToProto(richtext.Formatted{}); pb != nil {
		t.Errorf("no formatting went out as %+v", pb)
	}
}

// fillTime has no monotonic reading, so DeepEqual compares only the instant.
var fillTime = time.UnixMilli(1_700_000_000_123)

// fill puts a distinguishable non-zero value in every field reachable from v.
func fill(t *testing.T, v reflect.Value, path string) {
	t.Helper()

	if v.Type() == reflect.TypeFor[time.Time]() {
		v.Set(reflect.ValueOf(fillTime))
		return
	}
	// 7 is not a VerificationKind and would rightly be refused.
	if v.Type() == reflect.TypeFor[domain.VerificationKind]() {
		v.Set(reflect.ValueOf(domain.VerificationSAS))
		return
	}
	// Nor is 7 a way to leave a space, which reads back as not leaving it.
	if v.Type() == reflect.TypeFor[domain.SpaceLeaving]() {
		v.Set(reflect.ValueOf(domain.LeftAlone))
		return
	}

	// Formatting is built only by its constructors. It crosses the wire as it draws,
	// so it is made from two filled spans; its markup stays in the daemon by design.
	if v.Type() == reflect.TypeFor[richtext.Formatted]() {
		spans := reflect.New(reflect.TypeFor[[]richtext.Span]()).Elem()
		fill(t, spans, path+".Spans")
		v.Set(reflect.ValueOf(richtext.Drawn("filled:"+path, spans.Interface().([]richtext.Span))))
		return
	}

	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			fill(t, v.Field(i), path+"."+f.Name)
		}
	case reflect.String:
		v.SetString("filled:" + path)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(t, v.Elem(), path+"*")
	case reflect.Slice:
		// Two elements, so a converter that keeps only the first is caught too.
		s := reflect.MakeSlice(v.Type(), 2, 2)
		fill(t, s.Index(0), path+"[0]")
		fill(t, s.Index(1), path+"[1]")
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		fill(t, k, path+".key")
		val := reflect.New(v.Type().Elem()).Elem()
		fill(t, val, path+".value")
		m.SetMapIndex(k, val)
		v.Set(m)
	default:
		t.Fatalf("%s: fill does not know how to make a non-zero %s — teach it, "+
			"or this field is silently untested", path, v.Kind())
	}
}

// inUTC returns v with every time.Time in it moved to UTC.
func inUTC(v any) any {
	out := reflect.New(reflect.TypeOf(v)).Elem()
	out.Set(reflect.ValueOf(v))
	utcTimes(out)
	return out.Interface()
}

func utcTimes(v reflect.Value) {
	if v.Type() == reflect.TypeFor[time.Time]() {
		if v.CanSet() {
			v.Set(reflect.ValueOf(v.Interface().(time.Time).UTC()))
		}
		return
	}
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				utcTimes(v.Field(i))
			}
		}
	case reflect.Pointer:
		if !v.IsNil() {
			utcTimes(v.Elem())
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			utcTimes(v.Index(i))
		}
	}
}

// zeroNamed clears every field whose name is in names, at any depth.
func zeroNamed(v reflect.Value, names map[string]string) {
	switch v.Kind() {
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Time]() {
			return
		}
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			if _, off := names[f.Name]; off {
				v.Field(i).Set(reflect.Zero(f.Type))
				continue
			}
			zeroNamed(v.Field(i), names)
		}
	case reflect.Pointer:
		if !v.IsNil() {
			zeroNamed(v.Elem(), names)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			zeroNamed(v.Index(i), names)
		}
	}
}

// reportFirstDifference names the field that changed.
func reportFirstDifference(t *testing.T, got, want reflect.Value, path string) {
	t.Helper()

	if got.Kind() == reflect.Struct && got.Type() != reflect.TypeFor[time.Time]() {
		for i := range got.NumField() {
			if !got.Type().Field(i).IsExported() {
				continue
			}
			reportFirstDifference(t, got.Field(i), want.Field(i), path+"."+got.Type().Field(i).Name)
		}
		return
	}
	if !reflect.DeepEqual(got.Interface(), want.Interface()) {
		t.Errorf("  %s: got %v, want %v", path, got.Interface(), want.Interface())
	}
}
