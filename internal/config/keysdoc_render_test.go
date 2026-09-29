package config

// The [keys] part of default.toml is generated from keySections by the test that
// checks it (`make keys-doc` runs that test with -update), so the renderer is test code.

import (
	"strings"
	"unicode/utf8"
)

// keysTOMLStart opens the part of default.toml rendered from keySections, which runs
// from this line to the end of the file.
const keysTOMLStart = "# ─────────────────────────────────────────────────────────────────────────────\n# Keybindings."

// docWidth is the column default.toml's comments wrap at.
const docWidth = 88

// renderKeysTOML is the [keys] part of default.toml, rendered from keySections.
func renderKeysTOML() string {
	var b strings.Builder
	for i, section := range keySections {
		if i > 0 {
			b.WriteString("\n")
		}
		if i == 0 {
			b.WriteString(strings.SplitN(keysTOMLStart, "\n", 2)[0] + "\n")
		}
		writeComment(&b, section.intro)
		if section.table == "" {
			b.WriteString("[keys]\n")
		} else {
			b.WriteString("[keys." + section.table + "]\n")
		}
		writeBindings(&b, section.binds)
		if section.outro != "" {
			b.WriteString("\n")
			writeComment(&b, section.outro)
		}
	}
	return b.String()
}

// writeBindings renders one table's bindings, names and comments aligned. A doc that
// would run past docWidth goes on its own line above the binding instead.
func writeBindings(b *strings.Builder, binds []keyBinding) {
	nameWidth := 0
	for _, bind := range binds {
		nameWidth = max(nameWidth, len(bindingName(bind)))
	}
	lines := make([]string, len(binds))
	docColumn := 0
	for i, bind := range binds {
		name := bindingName(bind)
		lines[i] = name + strings.Repeat(" ", nameWidth-len(name)) + " = " + quoteTOML(bind.def)
		docColumn = max(docColumn, utf8.RuneCountInString(lines[i]))
	}
	for i, bind := range binds {
		overflows := docColumn+4+utf8.RuneCountInString(bind.doc) > docWidth
		if overflows {
			writeComment(b, bind.doc)
		}
		if bind.note != "" {
			writeComment(b, wrap(bind.note, docWidth-2))
		}
		if overflows {
			b.WriteString(lines[i] + "\n")
			continue
		}
		pad := docColumn - utf8.RuneCountInString(lines[i])
		b.WriteString(lines[i] + strings.Repeat(" ", pad) + "  # " + bind.doc + "\n")
	}
}

// bindingName is a binding's key within its table.
func bindingName(bind keyBinding) string {
	return bind.path[strings.LastIndex(bind.path, ".")+1:]
}

// writeComment writes text as TOML comment lines, a blank line as a bare "#".
func writeComment(b *strings.Builder, text string) {
	for line := range strings.SplitSeq(text, "\n") {
		if line == "" {
			b.WriteString("#\n")
			continue
		}
		b.WriteString("# " + line + "\n")
	}
}

// wrap fills text's words into lines of at most width runes.
func wrap(text string, width int) string {
	var lines []string
	line := ""
	for word := range strings.FieldsSeq(text) {
		switch {
		case line == "":
			line = word
		case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) > width:
			lines = append(lines, line)
			line = word
		default:
			line += " " + word
		}
	}
	return strings.Join(append(lines, line), "\n")
}
