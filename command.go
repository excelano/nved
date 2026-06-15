package main

import (
	"fmt"
	"os"
	"strconv"
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
	case "x", "exit", "q", "quit":
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
	// Any command other than a repeated exit disarms the exit warning.
	b.exitArmed = false
	if start, end, ok, matched := r.headTail(s); matched {
		if ok {
			r.printLines(start, end)
		} else {
			emit("nved: head/tail takes a positive line count — type h for help\n")
			r.last = nil
		}
		return false
	}
	if r.wrapDispatch(s) {
		return false
	}
	if r.dsvDispatch(s) {
		return false
	}
	if r.findDispatch(s) {
		return false
	}
	if start, end, ok := parseAddress(s, len(b.lines)); ok {
		r.printLines(start, end)
	} else {
		emitf("nved: unknown command %q — type h for help\n", s)
		r.last = nil
	}
	return false
}

// headTail parses the head/tail shorthands and returns the inclusive range to
// print. Bare "head"/"tail" show one screenful from the top / bottom (printLines
// caps a from-the-top range to a screenful; the bottom screenful is sized with
// visibleTail). An optional count — "head 30", "tail 10" — overrides it: "tail N"
// is the last N lines, which is why $-N addressing had to exist underneath. ok is
// false when the verb matched but the count was not a positive number; matched is
// false when s is not a head/tail command at all, so dispatch falls through to
// address parsing (a word like "tailor" matches neither).
func (r *repl) headTail(s string) (start, end int, ok, matched bool) {
	n := len(r.b.lines)
	for _, verb := range []string{"head", "tail"} {
		count, hasCount := 0, false
		switch {
		case s == verb:
			// no count
		case strings.HasPrefix(s, verb+" "):
			c, err := strconv.Atoi(strings.TrimSpace(s[len(verb):]))
			if err != nil || c < 1 {
				return 0, 0, false, true
			}
			count, hasCount = c, true
		default:
			continue
		}
		if verb == "head" {
			start = 1
			if hasCount {
				end = clamp(count, 1, n)
			} else {
				end = n // printLines caps this to one screenful from the top
			}
		} else { // tail
			end = n
			if hasCount {
				start = clamp(n-count+1, 1, n)
			} else {
				r.refreshSize()
				start = r.visibleTail(1, n) // top of the last screenful
			}
		}
		return start, end, true, true
	}
	return 0, 0, false, false
}

// wrapDispatch handles the wrap command — bare reports state, on/off sets it —
// and reports whether s was a wrap command so the main dispatch falls through to
// address parsing when it isn't. Like the DSV commands it clears r.last: the
// state report sits between the block and the prompt, so a climb key would no
// longer land on the right rows. wrap is orthogonal to the DSV layer, so it lives
// outside dsvDispatch.
func (r *repl) wrapDispatch(s string) bool {
	switch {
	case s == "wrap":
		emitf("wrap: %s\n", onOff(r.wrap))
	case strings.HasPrefix(s, "wrap "):
		onOffArg("wrap", strings.TrimPrefix(s, "wrap "), &r.wrap)
	default:
		return false
	}
	r.last = nil
	return true
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
		emit("nved: unsaved changes — exit again to discard, or save (s / save / Ctrl+S)\n")
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
	if fit := r.fillDown(start); end > fit {
		end = fit
	}
	out(csiWrapOff)
	if r.delim != 0 {
		r.lastAligned = r.printBlockAligned(start, end) // false when it falls back to raw
	} else {
		r.printBlockRaw(start, end)
		r.lastAligned = false
	}
	out(csiWrapOn)
	r.last = &block{start: start, count: end - start + 1}
}

// printBlockRaw prints [start,end] as plain text through the wrap-aware emitLine
// the editor uses — the default path, and the fallback when a DSV block can't be
// aligned. It leads with the faint status row, then one gutter-prefixed,
// word-wrapped block line per buffer line.
func (r *repl) printBlockRaw(start, end int) {
	w := r.gutterW()
	a := r.textWidth()
	out("\r" + csiEL + r.header(start, end) + "\r\n")
	for i := start; i <= end; i++ {
		text := r.b.lines[i-1]
		// Highlight the search match on this line, converting its raw rune range to
		// display columns the way visualCol lays text out (tabs expanded) — the same
		// mapping the cursor uses, so highlight and cursor agree. Both wrap and
		// wrap-off measure in those display columns.
		hlLo, hlHi := 0, 0
		if lo, hi, ok := r.matchRange(i); ok {
			hlLo, hlHi = visualCol(text, lo), visualCol(text, hi)
		}
		if r.wrap {
			emitLine(w, i, text, a, hlLo, hlHi)
		} else {
			// wrap off: one row per line, windowed at the left edge (the command-line
			// print never pans — only a climbed-in cursor does), with a faint › where
			// the line runs past the right edge.
			emitWindowedRow(w, i, 0, a, expandTabs(text), nil, false, hlLo, hlHi)
		}
	}
}

// undoAtPrompt reverses the most recent edit from the command line — the path
// for Ctrl+U pressed at the prompt, and for an in-editor Ctrl+U whose edit lies
// off-screen (the editor leaves with actUndo and run() lands here). It applies
// the inverse and reprints the affected line so the change is visible and ready
// to climb into; with nothing to undo it says so and clears r.last.
func (r *repl) undoAtPrompt() {
	entry, ok := r.b.popUndo()
	if !ok {
		emit("nved: nothing to undo\n")
		r.last = nil
		return
	}
	entry.apply(r.b)
	line := entry.line + 1 // printLines is 1-based
	r.printLines(line, line)
}

func printHelp() {
	emit(`nved — print a range of lines, climb into it, and edit it in place.

PRINTING
  N            line N
  N.M          lines N through M
  N.   .N      line N to the end / the start to line N
  .            every line
  $    $-N     the last line / the Nth line before the last
  head [N]     the first screenful, or the first N lines
  tail [N]     the last screenful, or the last N lines
  wrap on|off  word-wrap long lines, or one row each and pan sideways

  Page-Up / Page-Down reprint the screenful above / below. A , works in
  place of . in any range; out-of-range numbers clamp to the nearest line.

EDITING — climb into the printed block to change it
  climb in     Up (bottom line), Left (its end), or Ctrl+Home (first line)
  move         arrows; Ctrl+Left / Ctrl+Right by word (by field when aligned)
  jump         Home / End to line ends; Ctrl+Home / Ctrl+End to buffer ends
  change       type to insert; Enter splits a line; Backspace / Delete join
  undo         Ctrl+U — also at the prompt, and across climbing in and out
  leave        Esc, Ctrl+C, or step off the bottom (Down) or the end (Right)

SESSION
  s [name]     save; a name is required when unnamed (also Ctrl+S)
  x  q  quit   exit; warns once when there are unsaved edits (also Ctrl+X)
  h  ?         show this help

DELIMITED VIEW — opt-in; a file opens as plain text until you ask
  dsv C                split lines into columns on C (a char, or tab / unit)
  dsv off              back to plain text
  quotes on|off        respect "quoted,fields" when splitting
  headers on|off       pin line 1 as a faint column header
  rows newline|record  the record separator (record = ASCII 0x1E)
  csv  tsv  asv        presets; asv = unit fields, record rows (off undoes)

  A bare dsv / quotes / headers / rows / wrap reports its current state.
  In an aligned block, Tab / Shift-Tab move field to field and the grid
  re-aligns as you type. Editing changes the field value only — the
  delimiter (it is data inside a "quoted" cell), Enter-split, and row joins
  are suppressed; use dsv off for structural edits.
`)
}
