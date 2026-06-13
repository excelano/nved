package main

import (
	"fmt"
	"os"
	"strings"
)

// emit writes a line of output, translating "\n" to "\r\n" since OPOST is off
// in raw mode and a bare newline would not return the cursor to column 0.
func emit(s string) {
	os.Stdout.WriteString(strings.ReplaceAll(s, "\n", "\r\n"))
}

func emitf(format string, a ...any) {
	emit(fmt.Sprintf(format, a...))
}

// dispatch runs a single command line and returns true when nved should quit.
// Anything that prints below the block (a save notice, help, an error, an exit
// warning) clears r.last, since the block is no longer the thing directly above
// the prompt and a climb key would land on the wrong rows.
func (r *repl) dispatch(line string) bool {
	b := r.b
	s := strings.TrimSpace(line)
	if name, ok := saveArg(s); ok {
		b.exitArmed = false
		doSave(b, r.rd, name)
		r.last = nil
		return false
	}
	switch s {
	case "":
		b.exitArmed = false
		return false
	case "x", "exit":
		if doExit(b) {
			return true
		}
		r.last = nil
		return false
	case "h", "H", "?", "help":
		b.exitArmed = false
		printHelp()
		r.last = nil
		return false
	}
	// Any command other than a second consecutive x disarms the exit warning.
	b.exitArmed = false
	if start, end, ok := parseAddress(s, len(b.lines)); ok {
		r.printLines(start, end)
	} else {
		emitf("nved: unknown command %q — type h for help\n", s)
		r.last = nil
	}
	return false
}

// saveArg reports whether s is a save command — "s" or "save", optionally
// followed by a filename — and returns the filename (empty when none was given).
// "save" is checked before "s" so the longer verb wins; a bare word like "saved"
// matches neither and falls through to the normal dispatch.
func saveArg(s string) (name string, ok bool) {
	for _, verb := range []string{"save", "s"} {
		if s == verb {
			return "", true
		}
		if rest, found := strings.CutPrefix(s, verb+" "); found {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

// doSave writes the buffer to disk. An explicit name (from "s name") sets the
// buffer's file, so a later bare save goes to the same place. With no name given
// and an unnamed buffer the name is mandatory: it is prompted for, and the save
// is cancelled if none is entered.
func doSave(b *buffer, rd *reader, name string) {
	if name != "" {
		b.name = name
	}
	if b.name == "" {
		entered, ok := readLine(rd, "file name: ")
		entered = strings.TrimSpace(entered)
		if !ok || entered == "" {
			emit("save cancelled\n")
			return
		}
		b.name = entered
	}
	n, err := b.save()
	if err != nil {
		emitf("nved: %v\n", err)
		return
	}
	emitf("wrote %d lines to %s\n", n, b.name)
}

// doExit implements warn-twice: a clean buffer exits at once, a dirty buffer
// warns on the first x and exits (discarding) on the second. x never saves.
func doExit(b *buffer) bool {
	if !b.modified {
		return true
	}
	if !b.exitArmed {
		b.exitArmed = true
		emit("nved: unsaved changes — x again to discard, or s to save\n")
		return false
	}
	return true
}

// printLines prints an inclusive 1-based range, capped to a single screenful so
// the print never scrolls off the top: a range taller than the terminal is
// trimmed to the lines that fit from start downward (fillDown), and Page-Down
// reveals the rest. This lands the cursor at the TOP of what was asked for — not
// the bottom, as a cat-style dump would — and keeps the faint header on screen.
// The printed block is recorded as r.last so a later climb edits exactly these
// lines. It renders through the same wrap-aware emitLine the editor uses —
// gutter, continuation indent, word wrap — so a block looks the same here as
// once climbed into; the gutter is sized to the buffer's largest line number so
// a line sits at a fixed column regardless of the range. The faint header row
// leads the block — which lines show, of how many, plus the paging keys when
// there's somewhere to page. r.termW/termH are refreshed here so the block
// reflects the current terminal size, which the editor reuses on a later climb.
func (r *repl) printLines(start, end int) {
	r.refreshSize()
	w := r.gutterW()
	a := r.textWidth()
	if fit := r.fillDown(start); end > fit {
		end = fit
	}
	out(csiWrapOff)
	out("\r" + csiEL + r.header(start, end) + "\r\n")
	for i := start; i <= end; i++ {
		emitLine(w, i, r.b.lines[i-1], a)
	}
	out(csiWrapOn)
	r.last = &block{start: start, count: end - start + 1}
}

func printHelp() {
	emit(`nved commands
  N           print line N (numbered)
  N,M         print lines N..M
  N,   ,N     print N to the end / the start to N
  ,           print all lines
  $           print the last line
  s [name]    write buffer to disk; name required when unnamed  (Ctrl+S)
  x  exit     quit without saving       (Ctrl+X)
  h  help     show this help            (also H, ?)
out-of-range numbers clamp to the nearest valid line.
climb into the last printed block with Up / Left / Ctrl+Home to edit it;
Ctrl+Left / Ctrl+Right skip the cursor back / forward by words while editing;
Page-Up / Page-Down reprint the screenful above / below to climb into that;
Ctrl+U undoes the last edit (within the session); leave it with Esc, Ctrl+C,
or by stepping off the bottom (Down) or end (Right).
`)
}
