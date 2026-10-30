#!/usr/bin/env python3
"""Generate internal/emoji/generated.go from Unicode's emoji-test.txt, and
internal/emoji/slack.go from Slack's emoji names (`make emoji`); and the desktop's
copy of the tables it offers, desktop/src/gen/emoji.ts (curated.go's set beside the
generated ones), so its palette is instant and names what the TUI's names.

Standard: single-glyph, fully-qualified, no skin tone (kith applies tones).
Sequences: ZWJ sequences and flags — opt-in, since fonts without the ligature draw
them wider than measured. Shortcodes come from CLDR names; emoji.go's names win.
Slack: the names Slack knows each emoji by (iamcal/emoji-data, which Slack uses).
"""

from __future__ import annotations

import json
import re
import sys
import unicodedata
import urllib.request
from pathlib import Path

# Both sources are pinned to one emoji version, so `make emoji` writes the same tables
# until a new one is taken on purpose: Unicode's "latest" moves (and renames), and
# Slack's names are iamcal/emoji-data's release of that version.
EMOJI_VERSION = "16.0"
SOURCE = f"https://unicode.org/Public/emoji/{EMOJI_VERSION}/emoji-test.txt"
OUT = Path(__file__).resolve().parent.parent / "internal" / "emoji" / "generated.go"
SLACK_SOURCE = f"https://cdn.jsdelivr.net/npm/emoji-datasource@{EMOJI_VERSION}.0/emoji.json"
SLACK_OUT = OUT.with_name("slack.go")
CURATED = OUT.with_name("curated.go")
TS_OUT = OUT.parent.parent.parent / "desktop" / "src" / "gen" / "emoji.ts"

TONES = range(0x1F3FB, 0x1F400)
REGIONAL = range(0x1F1E6, 0x1F200)
ZWJ = 0x200D
KEYCAP = 0x20E3

# Newer emoji draw as 1-column tofu on lagging fonts, breaking layout. To raise it:
# bump, `make emoji`, then `go run ./cmd/emoji-probe -set complete` on your font.
# It caps both tables. Above 15.1 uniseg must also know the version: v0.4.7 measures
# Unicode 16.0's single emoji (🪉 🫟 🫩) one column wide, where terminals draw two.
MAX_EMOJI_VERSION = 15.1


def shortcode(name: str) -> str:
    """Turn a CLDR name into a typeable shortcode: lowercase words joined by _."""
    name = unicodedata.normalize("NFKD", name)
    name = name.replace("’", "").replace("“", "").replace("”", "")
    # "flag: Ukraine" and "keycap: #" both carry their distinguishing part after the
    # colon, so it is kept rather than dropped.
    words = re.split(r"[^0-9a-zA-Z]+", name.lower())
    return "_".join(w for w in words if w)


def classify(codepoints: list[int]) -> str:
    """Which tier a sequence belongs to, or "skip" for one we do not offer at all.

    Two whole classes are dropped, both because their *drawn* width is not the width
    anything measures them at — and a cell one column out wraps its row and shifts the
    pane (see cmd/emoji-probe):

      keycaps   `1️⃣` is base + VS16 + U+20E3 and measures two columns while terminals
                draw it in one. There are eleven of them and no way to reconcile it: the
                selector trick that fixes a text-presentation emoji cannot make a
                terminal draw *wider*.

      pre-toned every emoji here that already carries a Fitzpatrick modifier. For the
                single-glyph ones kith applies the tone itself from [display]
                skin_tone, so they are duplicates; for the ZWJ ones (⛹🏿‍♂️) they are
                both a fivefold multiplication of the set and — measured — the largest
                group that draws wider than it measures.
    """
    if KEYCAP in codepoints:
        return "skip"
    if any(cp in TONES for cp in codepoints):
        return "skip"
    if ZWJ in codepoints or any(cp in REGIONAL for cp in codepoints):
        return "sequence"
    return "standard"


def within_version(field: str) -> bool:
    """Whether an "E15.1" version field is one this generation includes."""
    try:
        return float(field.lstrip("Ee")) <= MAX_EMOJI_VERSION
    except ValueError:
        return False


def parse(text: str) -> tuple[list[tuple[str, str]], list[tuple[str, str]]]:
    standard: list[tuple[str, str]] = []
    sequence: list[tuple[str, str]] = []
    seen: set[str] = set()
    for line in text.splitlines():
        if not line.strip() or line.startswith("#"):
            continue
        codes, rest = line.split(";", 1)
        status, _, comment = rest.partition("#")
        if status.strip() != "fully-qualified":
            continue
        codepoints = [int(c, 16) for c in codes.split()]
        glyph = "".join(chr(cp) for cp in codepoints)
        # The comment is "<glyph> E<version> <name>": the version gates it, the name
        # becomes the shortcode.
        parts = comment.strip().split(" ", 2)
        if len(parts) < 3:
            continue
        if not within_version(parts[1]):
            continue
        name = shortcode(parts[2])
        if not name or name in seen:
            continue
        seen.add(name)
        kind = classify(codepoints)
        if kind == "skip":
            continue
        if kind == "standard":
            standard.append((name, glyph))
        elif kind == "sequence":
            sequence.append((name, glyph))
    return standard, sequence


def render(standard, sequence) -> str:
    def table(varname: str, rows, doc: str) -> str:
        lines = [doc, f"var {varname} = map[string]string{{"]
        for name, glyph in rows:
            lines.append(f'\t{name!r}: {glyph!r},'.replace("'", '"'))
        lines.append("}")
        return "\n".join(lines)

    header = f"""// Code generated by scripts/gen-emoji.py from {SOURCE}. DO NOT EDIT.
//
// Regenerate with `make emoji` after a new Unicode release. The file is committed so
// that a build needs no network; the script is a development tool.

package emoji
"""
    return "\n".join(
        [
            header,
            table(
                "Standard",
                standard,
                "// Standard is every fully-qualified single-glyph emoji: no ZWJ sequence, no\n"
                "// flag, and no pre-applied skin tone (kith applies the tone itself).",
            ),
            "",
            table(
                "Sequences",
                sequence,
                "// Sequences is the composed emoji — ZWJ sequences and flags. They are kept\n"
                "// apart because they are the class fonts disagree about: without the ligature a\n"
                "// font draws the parts side by side, which is wider than the cell it was measured\n"
                "// into, and one column out wraps the row.",
            ),
            "",
        ]
    )


def parse_slack(text: str) -> tuple[list[tuple[str, str]], list[tuple[str, str]]]:
    """Slack's name → emoji for every name, and emoji → the name Slack writes it
    with. An emoji is keyed without a trailing U+FE0F, in both its qualified and
    unqualified spelling; ones kith never offers (keycaps) are left out."""
    names: list[tuple[str, str]] = []
    written: dict[str, str] = {}
    for entry in sorted(json.loads(text), key=lambda e: e["sort_order"]):
        codepoints = [int(c, 16) for c in entry["unified"].split("-")]
        if classify(codepoints) == "skip" or not within_version(entry["added_in"]):
            continue
        glyph = "".join(chr(cp) for cp in codepoints)
        for name in entry["short_names"]:
            names.append((name, glyph))
        for spelling in (entry["unified"], entry.get("non_qualified")):
            if spelling:
                key = "".join(chr(int(c, 16)) for c in spelling.split("-")).removesuffix("\ufe0f")
                written.setdefault(key, entry["short_name"])
    return names, sorted(written.items(), key=lambda kv: kv[1])


def render_slack(names, written) -> str:
    def row(k: str, v: str) -> str:
        return f"\t{json.dumps(k, ensure_ascii=False)}: {json.dumps(v, ensure_ascii=False)},"

    return "\n".join(
        [
            f"""// Code generated by scripts/gen-emoji.py from {SLACK_SOURCE}. DO NOT EDIT.
//
// Regenerate with `make emoji`; the file is committed so that a build needs no network.

package emoji

// SlackNames is every name Slack knows an emoji by → the emoji.
var SlackNames = map[string]string{{""",
            *(row(k, v) for k, v in names),
            "}",
            "",
            "// SlackName is the name Slack writes each emoji with, keyed without a trailing",
            "// U+FE0F.",
            "var SlackName = map[string]string{",
            *(row(k, v) for k, v in written),
            "}",
            "",
        ]
    )


def parse_curated(text: str) -> list[tuple[str, str]]:
    """curated.go's Curated map, its "name": "emoji" pairs in source order."""
    body = text.split("var Curated = map[string]string{", 1)[1].split("\n}\n", 1)[0]
    return [(json.loads(f'"{k}"'), json.loads(f'"{v}"')) for k, v in re.findall(r'"((?:[^"\\]|\\.)*)":\s*"((?:[^"\\]|\\.)*)"', body)]


def render_ts(curated, standard, sequence) -> str:
    def table(name: str, doc: str, rows) -> list[str]:
        return [
            doc,
            f"export const {name}: readonly (readonly [string, string])[] = [",
            *(f"  [{json.dumps(k, ensure_ascii=False)}, {json.dumps(v, ensure_ascii=False)}]," for k, v in rows),
            "];",
            "",
        ]

    return "\n".join(
        [
            f"// Code generated by scripts/gen-emoji.py from internal/emoji/curated.go and {SOURCE}. DO NOT EDIT.",
            "//",
            "// The desktop's copy of internal/emoji's tables, [name, emoji] in order. Regenerate",
            "// with `make emoji`; internal/emoji's tests fail while it differs from the Go maps.",
            "",
            *table("curated", "/** The browsable set, with Slack's and GitHub's names (emoji.Curated). */", curated),
            *table("standard", "/** Every single-glyph emoji by its CLDR name, untoned (emoji.Standard). */", standard),
            *table("sequences", "/** ZWJ sequences and flags (emoji.Sequences). */", sequence),
        ]
    )


def read(src: str) -> str:
    if not src.startswith("http"):
        return Path(src).read_text(encoding="utf-8")
    try:
        return urllib.request.urlopen(src, timeout=30).read().decode("utf-8")  # noqa: S310
    except OSError as err:  # HTTPError (a 404 for a version not published) is one
        raise SystemExit(f"gen-emoji: cannot read {src}: {err}") from err


def main() -> int:
    src = sys.argv[1] if len(sys.argv) > 1 else SOURCE
    slack_src = sys.argv[2] if len(sys.argv) > 2 else SLACK_SOURCE
    # Both are read and checked before either table is written, so a failed fetch
    # leaves the committed pair as it was rather than one new and one old.
    text, slack_text = read(src), read(slack_src)
    if f"# Version: {EMOJI_VERSION}\n" not in text:
        raise SystemExit(f"gen-emoji: {src} is not emoji version {EMOJI_VERSION}")
    standard, sequence = parse(text)
    names, written = parse_slack(slack_text)
    if not standard or not names:
        raise SystemExit(f"gen-emoji: no emoji read from {src if not standard else slack_src}")
    OUT.write_text(render(standard, sequence), encoding="utf-8")
    print(f"{OUT.name}: {len(standard)} standard, {len(sequence)} sequence emoji")
    SLACK_OUT.write_text(render_slack(names, written), encoding="utf-8")
    print(f"{SLACK_OUT.name}: {len(names)} Slack names for {len(written)} emoji")
    curated = parse_curated(CURATED.read_text(encoding="utf-8"))
    TS_OUT.write_text(render_ts(curated, standard, sequence), encoding="utf-8")
    print(f"{TS_OUT.name}: {len(curated)} curated, {len(standard)} standard, {len(sequence)} sequence emoji")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
