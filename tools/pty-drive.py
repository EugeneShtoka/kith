#!/usr/bin/env python3
"""Run kith in a pseudo-terminal, drive it with keystrokes, and print the cell grid.

This exists because of a class of bug that neither a unit test nor a screenshot can
pin down. A unit test can only prove the client is *self-consistent* — that every row
it emits is as wide as it measured it to be. A screenshot shows what a terminal drew
but not why. What is missing between them is the client's real output, interpreted by
something that computes glyph widths and grapheme clusters the way a terminal does.

That gap is where the interesting failures live. When a row is wider on screen than
the client believed, it overflows its pane, wraps, and shifts every row below it — and
because Bubble Tea only rewrites lines it thinks changed, the shifted cells survive
into later frames. On screen that reads as messages duplicated, messages missing, or a
line from another room "stuck" in the conversation. It was found this way: a stray ♀
alone on a row turned out to be the tail of 🙋‍♀️, taken apart by an RTL reversal that
reversed runes instead of grapheme clusters (see reverseClusters in internal/tui).

    tools/pty-drive.py --settle 7 --keys j,j,j,j,tab,j,j,j,l,wait:2,esc
    tools/pty-drive.py --keys 'ctrl+f,wait:1,a,d,v,a' --rows-from 20

It talks to a running daemon, so it is the real client against the real account: keys
that open rooms send read receipts, and keys that send messages send messages. Drive
it with that in mind.

Needs pyte (`pip install pyte`), which is the terminal emulator doing the work.
"""

import argparse
import fcntl
import os
import pty
import select
import signal
import struct
import subprocess
import sys
import termios
import time

try:
    import pyte
except ImportError:  # pragma: no cover - a tool, not library code
    sys.exit("pty-drive: needs pyte — `pip install pyte` (a venv is fine)")

# Keys that are not a single character. Anything else is sent as typed, and "ctrl+x"
# is translated to the control byte for x.
NAMED = {
    "tab": b"\t",
    "enter": b"\r",
    "esc": b"\x1b",
    "space": b" ",
    "bs": b"\x7f",
    "up": b"\x1b[A",
    "down": b"\x1b[B",
    "right": b"\x1b[C",
    "left": b"\x1b[D",
    "pgup": b"\x1b[5~",
    "pgdn": b"\x1b[6~",
}


def keybytes(name):
    """Translate one step of a key script into what the terminal would send."""
    if name in NAMED:
        return NAMED[name]
    if name.startswith("ctrl+") and len(name) == 6:
        return bytes([ord(name[5].lower()) - ord("a") + 1])
    return name.encode()


class Terminal:
    """A pty with a terminal emulator on the far end of it."""

    def __init__(self, argv, cols, rows):
        self.cols, self.rows = cols, rows
        self.screen = pyte.Screen(cols, rows)
        self.stream = pyte.ByteStream(self.screen)
        self.master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
        env = dict(os.environ, TERM="xterm-256color", COLUMNS=str(cols), LINES=str(rows))
        self.proc = subprocess.Popen(argv, stdin=slave, stdout=slave, stderr=slave, env=env)
        os.close(slave)

    def pump(self, seconds):
        """Feed whatever the client writes into the emulator for a while."""
        end = time.monotonic() + seconds
        while time.monotonic() < end:
            readable, _, _ = select.select([self.master], [], [], 0.05)
            if not readable:
                continue
            try:
                data = os.read(self.master, 65536)
            except OSError:  # the child closed its end
                return
            if not data:
                return
            self.stream.feed(data)

    def send(self, data, wait):
        os.write(self.master, data)
        self.pump(wait)

    def grid(self):
        return [self.screen.display[i].rstrip() for i in range(self.rows)]

    def close(self):
        try:
            self.proc.send_signal(signal.SIGTERM)
            self.proc.wait(timeout=3)
        except Exception:
            self.proc.kill()


def repeated(grid, minimum=12):
    """Rows that appear more than once — the shape a shifted frame leaves behind.

    Short rows are ignored: blanks, borders and chrome repeat by design, and a rule
    that flagged them would bury the one line that matters.
    """
    seen = {}
    for i, row in enumerate(grid):
        text = row.strip()
        if len(text) < minimum:
            continue
        seen.setdefault(text, []).append(i)
    return {text: at for text, at in seen.items() if len(at) > 1}


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--cmd", default=os.path.expanduser("~/.local/bin/kith"), help="client to run")
    ap.add_argument("--cols", type=int, default=250, help="terminal width (default: 250)")
    ap.add_argument("--rows", type=int, default=60, help="terminal height (default: 60)")
    ap.add_argument("--settle", type=float, default=7.0, help="seconds to wait for the first frame")
    ap.add_argument("--keys", default="", help="comma-separated keys: j,tab,esc,ctrl+f,wait:2 …")
    ap.add_argument("--pause", type=float, default=0.5, help="seconds after each key")
    ap.add_argument("--rows-from", type=int, default=0, help="first grid row to print")
    ap.add_argument("--rows-to", type=int, default=None, help="last grid row to print")
    ap.add_argument("--every-key", action="store_true", help="print the grid after each key, not only at the end")
    args = ap.parse_args()

    term = Terminal([args.cmd], args.cols, args.rows)
    term.pump(args.settle)

    def dump(label):
        print(f"\n===== {label} =====")
        for i, row in enumerate(term.grid()):
            if not row:
                continue
            if i < args.rows_from or (args.rows_to is not None and i > args.rows_to):
                continue
            print(f"{i:3d}| {row}")

    if args.every_key:
        dump("startup")
    for step in (s for s in args.keys.split(",") if s):
        if step.startswith("wait:"):
            term.pump(float(step[5:]))
            continue
        term.send(keybytes(step), args.pause)
        if args.every_key:
            dump(f"after {step}")
    if not args.every_key:
        dump("final")

    dupes = repeated(term.grid())
    print("\n-- rows drawn more than once --")
    print("none" if not dupes else "\n".join(f"rows {at}: {text[:100]}" for text, at in dupes.items()))
    term.close()
    # Non-zero when the screen holds a repeated row, so this can gate a check rather
    # than only inform one.
    sys.exit(1 if dupes else 0)


if __name__ == "__main__":
    main()
