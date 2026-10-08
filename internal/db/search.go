package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// searchQuery builds the search statement, or reports there is nothing to ask.
func searchQuery(req domain.SearchRequest) (string, []any, bool) {
	if req.Rooms.None() {
		return "", nil, false
	}
	match := ftsQuery(req.Filter.Terms)
	if match == "" {
		// With no terms the FTS join is dropped ("everything Dana said last week").
		if req.Filter.Empty() {
			return "", nil, false
		}
		where, args, ok := searchWhere(req, nil)
		if !ok {
			return "", nil, false
		}
		// The body stands in for the snippet so the scan keeps one shape.
		return `
		SELECT m.room_id, m.event_id, m.sender, m.sender_name, m.ts_ms, m.body,
		       COALESCE(mm.name, ''), m.body
		` + plainFrom(req.Filter) + " WHERE m.redacted = 0" + where + newestFirst + " LIMIT ?", append(args, req.Limit), true
	}
	where, args, ok := searchWhere(req, []any{match})
	if !ok {
		return "", nil, false
	}
	// A snippet is computed as a row is, before the sort: for a common word, every match
	// would be tokenized to show a page. So the page is found first, then excerpted.
	return `
		WITH page AS MATERIALIZED (
		  SELECT m.rowid AS id
		    FROM messages_fts f
		    JOIN messages m ON m.rowid = f.rowid
		    LEFT JOIN message_media mm ON mm.room_id = m.room_id AND mm.event_id = m.event_id
		   WHERE messages_fts MATCH ? AND m.redacted = 0` + where + newestFirst + ` LIMIT ?),
		excerpt AS MATERIALIZED (
		  SELECT rowid AS id, snippet(messages_fts, 0, ?, ?, '…', ?) AS snippet
		    FROM messages_fts
		   WHERE messages_fts MATCH ? AND +rowid IN (SELECT id FROM page))
		SELECT m.room_id, m.event_id, m.sender, m.sender_name, m.ts_ms,
		       e.snippet, COALESCE(mm.name, ''), m.body
		  FROM excerpt e
		  CROSS JOIN messages m ON m.rowid = e.id
		  LEFT JOIN message_media mm ON mm.room_id = m.room_id AND mm.event_id = m.event_id` + newestFirst,
		append(args, req.Limit, domain.HighlightStart, domain.HighlightEnd, snippetTokens, match), true
}

// newestFirst is a search's order: newestFirstIn across rooms.
const newestFirst = " ORDER BY" + newestFirstIn

// searchWhere is the conditions a request puts on m (and mm), each opening with AND,
// with their values appended to args; false when they can match nothing.
func searchWhere(req domain.SearchRequest, args []any) (string, []any, bool) {
	var sql string
	if !req.Rooms.All {
		var in string
		in, args = inIDs(args, req.Rooms.IDs)
		// #nosec G202 -- inIDs emits only placeholders; every room ID is bound.
		sql += " AND m.room_id" + in
	}
	// A sender matches the MXID or the display name.
	if sender := req.Filter.Sender; sender != "" {
		sql += ` AND (m.sender LIKE ? ESCAPE '\' OR m.sender_name LIKE ? ESCAPE '\')`
		like := "%" + escapeLike(sender) + "%"
		args = append(args, like, like)
	}
	if req.Filter.Mentioned {
		sql += " AND m.mentioned = 1"
	}
	if req.Filter.HasFile {
		sql += " AND mm.event_id IS NOT NULL"
	}
	if req.Filter.Tracked {
		clause, words, ok := trackedClause(req.Filter.Words)
		if !ok {
			return "", nil, false
		}
		sql += clause
		args = append(args, words...)
	}
	// EXISTS, not a join, so a starred message later deleted is kept.
	if req.Filter.Starred {
		sql += " AND EXISTS (SELECT 1 FROM starred s WHERE s.room_id = m.room_id AND s.event_id = m.event_id)"
	}
	if !req.Filter.Since.IsZero() {
		sql += " AND m.ts_ms >= ?"
		args = append(args, req.Filter.Since.UnixMilli())
	}
	if !req.Filter.Until.IsZero() {
		sql += " AND m.ts_ms < ?"
		args = append(args, req.Filter.Until.UnixMilli())
	}
	return sql, args, true
}

// plainFrom is a search's FROM without terms. Starred messages and files are few: the
// small table drives (CROSS JOIN fixes the order), where walking every message newest
// first would read the whole cache to fill a page.
func plainFrom(filter domain.SearchFilter) string {
	const media = `
		LEFT JOIN message_media mm ON mm.room_id = m.room_id AND mm.event_id = m.event_id`
	switch {
	case filter.Starred:
		return `FROM starred s CROSS JOIN messages m ON m.room_id = s.room_id AND m.event_id = s.event_id` + media
	case filter.HasFile:
		return `FROM message_media mm CROSS JOIN messages m ON m.room_id = mm.room_id AND m.event_id = mm.event_id`
	default:
		return `FROM messages m` + media
	}
}

// trackedClause narrows to messages containing any tracked word as a substring
// (a superset; SearchMessages applies domain.Tracked's word rule). False when
// nothing is tracked, which must match nothing rather than drop the filter.
func trackedClause(words []string) (string, []any, bool) {
	likes := make([]string, 0, len(words))
	args := make([]any, 0, len(words))
	for _, word := range words {
		needle := strings.Trim(strings.TrimSpace(word), "*")
		if needle == "" {
			continue
		}
		likes = append(likes, `m.body LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(needle)+"%")
	}
	if len(likes) == 0 {
		return "", nil, false
	}
	return " AND (" + strings.Join(likes, " OR ") + ")", args, true
}

// snippetTokens is the tokens of context a result excerpt carries.
const snippetTokens = 12

// SearchMessages finds cached non-redacted messages matching req, newest first. A
// request with nothing searchable returns nothing and no error.
func (c *Cache) SearchMessages(ctx context.Context, req domain.SearchRequest) ([]domain.SearchHit, error) {
	query, args, ok := searchQuery(req)
	if !ok {
		return nil, nil
	}
	words := domain.Tracked{Words: req.Filter.Words}
	tracked := req.Filter.Tracked && words.Any()
	hits, err := collect(ctx, c.db, fmt.Sprintf("search hits for %q", req.Filter.Terms), query,
		func(rows *sql.Rows) (domain.SearchHit, error) {
			var (
				hit  domain.SearchHit
				ts   int64
				body string
			)
			err := rows.Scan(&hit.RoomID, &hit.EventID, &hit.Sender, &hit.SenderName, &ts,
				&hit.Snippet, &hit.FileName, &body)
			hit.Timestamp = time.UnixMilli(ts)
			if tracked {
				if found := words.Find(body); len(found) > 0 {
					hit.Word = found[0].Word
				}
			}
			return hit, err
		}, args...)
	if err != nil || !tracked {
		return hits, err
	}
	// Drop LIKE hits the word matcher rejects.
	kept := make([]domain.SearchHit, 0, len(hits))
	for i := range hits {
		if hits[i].Word != "" {
			kept = append(kept, hits[i])
		}
	}
	return kept, nil
}

// ftsQuery turns input into an FTS5 MATCH expression, or "". Every token is quoted
// as a literal (FTS5 syntax errors on "?", "(", "-x"), "quoted phrases" stay whole,
// and the last unquoted token is prefix-matched for search-as-you-type.
func ftsQuery(input string) string {
	tokens, lastIsPhrase := splitSearchTokens(input)
	if len(tokens) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tokens))
	for i, tok := range tokens {
		quoted := quoteFTS(tok)
		if i == len(tokens)-1 && !lastIsPhrase {
			quoted += "*" // prefix-match the word still being typed
		}
		parts = append(parts, quoted)
	}
	return strings.Join(parts, " ")
}

// splitSearchTokens splits input into terms, keeping "quoted phrases" whole;
// lastIsPhrase reports a closed final phrase.
func splitSearchTokens(input string) (tokens []string, lastIsPhrase bool) {
	var current strings.Builder
	inPhrase, closed := false, false
	flush := func(wasPhrase bool) {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			lastIsPhrase = wasPhrase
			current.Reset()
		}
	}
	for _, r := range input {
		switch {
		case r == '"':
			if inPhrase {
				flush(true)
				closed = true
			} else {
				flush(false)
				closed = false
			}
			inPhrase = !inPhrase
		case !inPhrase && unicode.IsSpace(r):
			flush(false)
		default:
			current.WriteRune(r)
		}
	}
	// An unterminated phrase is still flushed.
	flush(inPhrase && closed)
	return tokens, lastIsPhrase
}

// quoteFTS wraps s as an FTS5 string literal, doubling embedded quotes.
func quoteFTS(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// placeholders is "?, ?, ?" for an IN clause of n values.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// escapeLike escapes LIKE wildcards (use with ESCAPE '\').
func escapeLike(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return r.Replace(s)
}
