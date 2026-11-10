// Package chatexport reads a chat a phone exported (WhatsApp's Export chat) and matches
// it to the rooms and people kith knows. It reads; the daemon saves.
package chatexport

import (
	"archive/zip"
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Line is one message of an export.
type Line struct {
	At time.Time
	// Minute marks a time written without seconds (Android's): At is the minute's start.
	Minute bool
	Sender string
	Body   string
}

// Export is a chat as a phone wrote it.
type Export struct {
	Title string // the file's name without its extension
	Lines []Line // as the file has them, oldest first
}

// most is the most text read out of one export: years of a busy chat are a few MiB.
const most = 256 << 20

var (
	// ErrNoChat is a file that holds no chat a phone exported.
	ErrNoChat = errors.New("chatexport: the file holds no exported chat")
	errLarge  = fmt.Errorf("chatexport: the chat is over %d MiB of text", most>>20)
)

// Read reads the export at path, a zip as the phone shares it or its text alone; name
// is the file's own. Times are read in zone, which the phone wrote them in.
func Read(path, name string, zone *time.Location) (Export, error) {
	text, err := chatText(path)
	if err != nil {
		return Export{}, err
	}
	lines := parse(text, zone)
	if len(lines) == 0 {
		return Export{}, ErrNoChat
	}
	return Export{Title: strings.TrimSuffix(name, filepath.Ext(name)), Lines: lines}, nil
}

// chatText is the chat's text: the zip's _chat.txt (iOS) or only .txt (Android), else
// the file itself. Nothing is unpacked to disk.
func chatText(path string) (string, error) {
	z, err := zip.OpenReader(path)
	if errors.Is(err, zip.ErrFormat) {
		f, ferr := os.Open(filepath.Clean(path))
		if ferr != nil {
			return "", fmt.Errorf("chatexport: %w", ferr)
		}
		defer func() { _ = f.Close() }() // read only
		return readMost(f)
	}
	if err != nil {
		return "", fmt.Errorf("chatexport: %w", err)
	}
	defer func() { _ = z.Close() }() // read only
	var texts []*zip.File
	for _, f := range z.File {
		if filepath.Base(f.Name) == "_chat.txt" {
			texts = []*zip.File{f}
			break
		}
		if strings.EqualFold(filepath.Ext(f.Name), ".txt") {
			texts = append(texts, f)
		}
	}
	if len(texts) != 1 {
		return "", ErrNoChat
	}
	r, err := texts[0].Open()
	if err != nil {
		return "", fmt.Errorf("chatexport: %w", err)
	}
	defer func() { _ = r.Close() }() // read only
	return readMost(r)
}

// readMost reads r whole, refusing more than most.
func readMost(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, most+1))
	if err != nil {
		return "", fmt.Errorf("chatexport: %w", err)
	}
	if len(b) > most {
		return "", errLarge
	}
	return string(b), nil
}

// header starts a message: iOS's "[31/12/2020, 21:05:07] ", Android's
// "31/12/2020, 21:05 - ", in the phone's date order and clock, then what follows.
var header = regexp.MustCompile(`^\[?(\d{1,4})[./-](\d{1,2})[./-](\d{1,4}),?[\s\x{202f}\x{a0}]+` +
	`(\d{1,2}):(\d{2})(?::(\d{2}))?(?:[\s\x{202f}\x{a0}]*([AaPp])\.?[\s\x{202f}\x{a0}]?[Mm]\.?)?` +
	`(?:\][\s\x{202f}\x{a0}]|[\s\x{202f}\x{a0}][-–][\s\x{202f}\x{a0}])(.*)$`)

// marks are what a phone writes that no one reads: direction marks before names and
// notes, a byte-order mark, Windows line ends.
var marks = strings.NewReplacer("\u200e", "", "\ufeff", "", "\r", "")

// stamp is one header's fields as written: the date's three numbers in the phone's
// order, the clock, and what follows.
type stamp struct {
	date              [3]int
	hour, minute, sec int
	seconds, pm, am12 bool
	rest              string
	continued         []string
}

// parse reads the chat's messages. A line without a header continues the message
// before; a header without "Name: " is the phone's own note (encryption, a member
// added), which is left out.
func parse(text string, zone *time.Location) []Line {
	stamps := scan(text)
	ymd, dayFirst := dateOrder(stamps)
	var out []Line
	for i := range stamps {
		if l, ok := stamps[i].line(ymd, dayFirst, zone); ok {
			out = append(out, l)
		}
	}
	return out
}

// scan is the chat's headers as written, each with the lines continuing it.
func scan(text string) []stamp {
	var stamps []stamp
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64<<10), most)
	for sc.Scan() {
		line := marks.Replace(sc.Text())
		m := header.FindStringSubmatch(line)
		if m == nil {
			if len(stamps) > 0 {
				stamps[len(stamps)-1].continued = append(stamps[len(stamps)-1].continued, line)
			}
			continue
		}
		s := stamp{rest: m[8], seconds: m[6] != ""}
		for i := range 3 {
			s.date[i], _ = strconv.Atoi(m[i+1]) // digits, by the pattern
		}
		s.hour, _ = strconv.Atoi(m[4])
		s.minute, _ = strconv.Atoi(m[5])
		s.sec, _ = strconv.Atoi(m[6])
		switch strings.ToLower(m[7]) {
		case "p":
			s.pm = true
		case "a":
			s.am12 = s.hour == 12
		}
		stamps = append(stamps, s)
	}
	return stamps
}

// line is the message s starts, its date in the order given; false for a note.
func (s *stamp) line(ymd, dayFirst bool, zone *time.Location) (Line, bool) {
	sender, body, ok := strings.Cut(s.rest, ": ")
	if !ok {
		return Line{}, false
	}
	var y, mo, d int
	switch {
	case ymd:
		y, mo, d = s.date[0], s.date[1], s.date[2]
	case dayFirst:
		d, mo, y = s.date[0], s.date[1], s.date[2]
	default:
		mo, d, y = s.date[0], s.date[1], s.date[2]
	}
	if y < 100 {
		y += 2000
	}
	hour := s.hour
	if s.pm && hour < 12 {
		hour += 12
	}
	if s.am12 {
		hour = 0
	}
	return Line{
		At:     time.Date(y, time.Month(mo), d, hour, s.minute, s.sec, 0, zone),
		Minute: !s.seconds,
		Sender: strings.TrimSpace(strings.TrimLeft(sender, "~\u202f\u00a0 ")),
		Body:   strings.Join(append([]string{body}, s.continued...), "\n"),
	}, true
}

// dateOrder is how the phone wrote dates: year first when the first number has four
// digits, else day first unless a month would pass 12 that way.
func dateOrder(stamps []stamp) (ymd, dayFirst bool) {
	dayFirst = true
	for i := range stamps {
		s := &stamps[i]
		if s.date[0] > 31 {
			return true, false
		}
		if s.date[0] > 12 {
			return false, true // a day cannot be the month
		}
		if s.date[1] > 12 {
			dayFirst = false
		}
	}
	return false, dayFirst
}
