// Command emoji-probe checks that every emoji kith offers occupies as many columns
// on screen as the client (uniseg) thinks: a one-column disagreement wraps a pane row
// and shifts everything below, and only the terminal itself can answer. It prints a
// glyph, reads the cursor position (DSR/CPR) and compares.
//
//	go run ./cmd/emoji-probe            # the configured set
//	go run ./cmd/emoji-probe -set complete -tone light
//	go run ./cmd/emoji-probe -text "…"  # one line, reported per rune
//
// It needs /dev/tty, prints one line per disagreement, and exits non-zero if any.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
	"github.com/EugeneShtoka/kith/internal/tui"
)

func main() {
	set := flag.String("set", "", `emoji set: curated, standard or complete (default: the config's)`)
	tone := flag.String("tone", "", `skin tone: none, light … dark (default: the config's)`)
	text := flag.String("text", "", "measure this text instead of the emoji set (\"-\" reads stdin)")
	flag.Parse()

	if *text != "" {
		if err := runText(*text); err != nil {
			fmt.Fprintln(os.Stderr, "emoji-probe:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*set, *tone); err != nil {
		fmt.Fprintln(os.Stderr, "emoji-probe:", err)
		os.Exit(1)
	}
}

func run(setName, toneName string) error {
	cfg, path, err := loadConfig()
	if err != nil {
		return err
	}
	if setName != "" {
		cfg.Display.Emoji.Set = setName
	}
	if toneName != "" {
		cfg.Display.SkinTone = toneName
	}
	if verr := setup.Validate(cfg); verr != nil {
		return fmt.Errorf("config: %w", verr)
	}
	cells, err := tui.EmojiCells(cfg.Display)
	if err != nil {
		return fmt.Errorf("emoji cells: %w", err)
	}
	fmt.Printf("probing %d cells (set=%s tone=%s, config %s)\n",
		len(cells), orDefault(cfg.Display.Emoji.Set, "curated"),
		orDefault(cfg.Display.SkinTone, "none"), path)

	tty, closeTTY, err := openRaw()
	if err != nil {
		return err
	}
	defer closeTTY()

	bad := 0
	for _, cell := range cells {
		drawn, perr := probe(tty, cell)
		if perr != nil {
			return perr
		}
		if want := ansi.StringWidth(cell); drawn != want {
			bad++
			// Findings go to stdout (redirectable); only the cursor exchange uses the tty.
			fmt.Printf("mismatch %q measured=%d drawn=%d\n", cell, want, drawn)
		}
	}
	fmt.Printf("%d of %d cells disagree with the terminal\n", bad, len(cells))
	if bad > 0 {
		return fmt.Errorf("%d emoji would break the layout", bad)
	}
	return nil
}

// runText measures a whole line against the terminal, then each rune, so a
// disagreement names the character that caused it.
func runText(text string) error {
	if text == "-" {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		text = strings.TrimRight(string(raw), "\n")
	}
	tty, closeTTY, err := openRaw()
	if err != nil {
		return err
	}
	defer closeTTY()

	drawn, err := probe(tty, text)
	if err != nil {
		return err
	}
	want := ansi.StringWidth(text)
	fmt.Printf("whole line: measured=%d drawn=%d\n", want, drawn)

	bad := 0
	for _, r := range text {
		glyph := string(r)
		d, perr := probe(tty, glyph)
		if perr != nil {
			return perr
		}
		if w := ansi.StringWidth(glyph); d != w {
			bad++
			fmt.Printf("mismatch U+%04X %q measured=%d drawn=%d\n", r, glyph, w, d)
		}
	}
	fmt.Printf("%d rune(s) disagree with the terminal\n", bad)
	if bad > 0 || drawn != want {
		return errors.New("this text would break the layout")
	}
	return nil
}

// openRaw opens /dev/tty in raw mode (the cursor report arrives as unechoed input).
func openRaw() (*os.File, func(), error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("this needs a real terminal: %w", err)
	}
	state, err := term.MakeRaw(int(tty.Fd()))
	if err != nil {
		_ = tty.Close() // the raw-mode error is the report
		return nil, nil, fmt.Errorf("raw mode: %w", err)
	}
	// Restoring on the way out: nothing is left to tell if the terminal is gone.
	return tty, func() {
		_ = term.Restore(int(tty.Fd()), state)
		_ = tty.Close()
	}, nil
}

// probe prints cell at the start of a cleared line and returns the column the cursor
// reached — the terminal's own answer for how wide it is.
func probe(tty *os.File, cell string) (int, error) {
	if _, err := fmt.Fprintf(tty, "\x1b[H\x1b[2J%s\x1b[6n", cell); err != nil {
		return 0, fmt.Errorf("write to the terminal: %w", err)
	}
	// The reply is ESC [ row ; col R.
	var buf []byte
	one := make([]byte, 1)
	for {
		n, err := tty.Read(one)
		if err != nil {
			return 0, fmt.Errorf("read the cursor report: %w", err)
		}
		if n == 0 {
			continue
		}
		if one[0] == 'R' {
			break
		}
		buf = append(buf, one[0])
	}
	reply := string(buf)
	at := strings.LastIndex(reply, ";")
	if at < 0 {
		return 0, fmt.Errorf("unreadable cursor report %q", reply)
	}
	col, err := strconv.Atoi(reply[at+1:])
	if err != nil {
		return 0, fmt.Errorf("unreadable column in %q: %w", reply, err)
	}
	return col - 1, nil // the cursor sits one past what was drawn
}

func loadConfig() (config.Config, string, error) {
	path, err := config.Path()
	if err != nil {
		return config.Config{}, "", fmt.Errorf("resolve config path: %w", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, path, fmt.Errorf("load config: %w", err)
	}
	return cfg, path, nil
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
