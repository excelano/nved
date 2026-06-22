package main

import (
	"fmt"
	"strconv"
	"strings"
)

// The structural layer: insert and kill whole rows and columns. A column op
// rewrites every record (split into raw cells, add or remove one, rejoin verbatim
// with the delimiter, so a quoted field round-trips); a row op splices the line
// slice. Columns are dsv-only and carry no on-screen address — lines have a gutter
// number, columns don't — so a column is named by a spreadsheet letter (A, B, …),
// surfaced on demand by the `columns` letter ruler. Rows ARE the primary axis and
// already carry gutter numbers, so they need no ruler and work in plain text and DSV
// alike — in aligned view a row insert adds a well-formed empty record rather than
// re-enabling the unsafe mid-field Enter-split the cell editor suppresses.
//
// The verb leads the command — `insert column` / `kill row` — so the short forms
// are ic / kc / ir / kr, whose initials stay clear of the f / r / c verb families
// (find, replace, columns) where ri / ci would have blurred. No chords in v1:
// every mnemonic key was taken (Insert is eaten as Copy, Ctrl+I *is* Tab).

// structDispatch handles the structural family — insert / kill of a row or column,
// the short forms ic / kc / ir / kr, and the bare nouns (columns / c prints the
// letter ruler, rows reports the line count) — and reports whether s was one so the
// main dispatch falls through to address parsing when it isn't. It manages r.last
// itself: a report or a successful edit reprints a block, an error clears it.
func (r *repl) structDispatch(s string) bool {
	// Short forms first — own tokens, like fn / rn.
	if rest, ok := cutVerb(s, "ic"); ok {
		r.insertColumn(strings.TrimSpace(rest))
		return true
	}
	if rest, ok := cutVerb(s, "kc"); ok {
		r.killColumn(strings.TrimSpace(rest))
		return true
	}
	if rest, ok := cutVerb(s, "ir"); ok {
		r.insertRow(strings.TrimSpace(rest))
		return true
	}
	if rest, ok := cutVerb(s, "kr"); ok {
		r.killRow(strings.TrimSpace(rest))
		return true
	}
	// Long forms: insert <noun> [arg] / kill <noun> <arg>.
	if rest, ok := cutVerb(s, "insert"); ok {
		return r.structVerb("insert", rest)
	}
	if rest, ok := cutVerb(s, "kill"); ok {
		return r.structVerb("kill", rest)
	}
	// Bare nouns report state.
	switch s {
	case "columns", "c":
		r.reportColumns()
		return true
	case "rows":
		r.reportRows()
		return true
	}
	return false
}

// structVerb routes insert / kill to the row or column handler by its noun
// (singular or plural accepted). A missing or unknown noun is reported with a
// nudge, since the verb on its own is ambiguous.
func (r *repl) structVerb(verb, rest string) bool {
	noun, arg := firstWord(rest)
	switch noun {
	case "column", "columns":
		if verb == "insert" {
			r.insertColumn(arg)
		} else {
			r.killColumn(arg)
		}
	case "row", "rows":
		if verb == "insert" {
			r.insertRow(arg)
		} else {
			r.killRow(arg)
		}
	case "":
		emitf("nved: %s needs a target — %s row or %s column\n", verb, verb, verb)
		r.last = nil
	default:
		emitf("nved: %s what? — %s row or %s column\n", verb, verb, verb)
		r.last = nil
	}
	return true
}

// firstWord splits s into its first whitespace-delimited word and the trimmed
// remainder, both empty when s is blank.
func firstWord(s string) (word, rest string) {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

// --- columns ---------------------------------------------------------------

// columnLabel renders a 0-based column index as its spreadsheet letter — 0→A,
// 25→Z, 26→AA, 27→AB — the bijective base-26 scheme Excel uses, where there is no
// zero digit so AA follows Z rather than the A0 a plain base-26 would give.
func columnLabel(idx int) string {
	var b []byte
	for idx >= 0 {
		b = append(b, byte('A'+idx%26))
		idx = idx/26 - 1
	}
	// The letters came out least-significant first; reverse them.
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}

// parseColumnLabel parses a spreadsheet column letter (case-insensitive, one or
// more letters: A, z, AA) into its 0-based index, the inverse of columnLabel. It
// returns false for an empty string or any non-letter, so a stray number or
// punctuation is rejected rather than silently coerced.
func parseColumnLabel(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	idx := 0
	for _, c := range s {
		switch {
		case c >= 'A' && c <= 'Z':
			idx = idx*26 + int(c-'A') + 1
		case c >= 'a' && c <= 'z':
			idx = idx*26 + int(c-'a') + 1
		default:
			return 0, false
		}
	}
	return idx - 1, true // the running value is 1-based (bijective); shift to 0-based
}

// insertColumn adds one empty column. A bare arg appends it at the far right; a
// column letter L inserts the new empty column before L — so `ic A` prepends and
// `ic C` slips one in ahead of C, the "the new column takes this letter" mental
// model. Every parseable row gains a cell; a short ragged row gets it at its own
// end. The buffer line count is unchanged — only columns move.
func (r *repl) insertColumn(arg string) {
	if r.delim == 0 {
		emit("columns: not in delimited view — set a delimiter first (dsv C)\n")
		r.last = nil
		return
	}
	appendEnd := arg == ""
	pos := 0
	if !appendEnd {
		idx, ok := parseColumnLabel(arg)
		if !ok {
			emitf("columns: insert takes a column letter (A, B, …), not %q\n", arg)
			r.last = nil
			return
		}
		if max := r.columnCount(); idx >= max {
			emitf("columns: no column %s — columns run A–%s, or bare ic to append\n", columnLabel(idx), columnLabel(max-1))
			r.last = nil
			return
		}
		pos = idx // insert the new column before column idx
	}
	changed, ok := r.rewriteColumns(func(cells []string) ([]string, bool) {
		p := pos
		if appendEnd {
			p = len(cells)
		}
		return insertColumnCells(cells, p), true
	})
	if !ok {
		return // rewriteColumns reported the parse failure and cleared the block
	}
	var msg string
	switch {
	case appendEnd:
		msg = "columns: appended a column"
	case pos == 0:
		msg = "columns: prepended a column"
	default:
		msg = fmt.Sprintf("columns: inserted a column before %s", columnLabel(pos))
	}
	r.commitColumnEdit(changed, msg)
}

// killColumn removes column L from every row. L is a required column letter; a
// bare kill errors, since the op is destructive across the whole buffer and must
// name its target. It confirms first — naming the column by header when headers are
// on — and a row that has no column L is left untouched.
func (r *repl) killColumn(arg string) {
	if r.delim == 0 {
		emit("columns: not in delimited view — set a delimiter first (dsv C)\n")
		r.last = nil
		return
	}
	if arg == "" {
		emit("columns: kill needs a column letter — type columns to see them\n")
		r.last = nil
		return
	}
	idx, valid := parseColumnLabel(arg)
	if !valid {
		emitf("columns: kill takes a column letter (A, B, …), not %q\n", arg)
		r.last = nil
		return
	}
	if max := r.columnCount(); idx >= max {
		emitf("columns: no column %s — columns run A–%s\n", columnLabel(idx), columnLabel(max-1))
		r.last = nil
		return
	}
	col := columnLabel(idx)
	label := ""
	if r.headers {
		if name := r.headerName(idx + 1); name != "" {
			label = fmt.Sprintf(" %q", name)
		}
	}
	ans, ok := readLine(r.rd, fmt.Sprintf("kill column %s%s in every row? [y/N] ", col, label))
	if !ok || !isYes(ans) {
		emit("columns: kill cancelled\n")
		r.last = nil
		return
	}
	changed, pok := r.rewriteColumns(func(cells []string) ([]string, bool) {
		return deleteColumnCells(cells, idx)
	})
	if !pok {
		return
	}
	r.commitColumnEdit(changed, fmt.Sprintf("columns: killed column %s", col))
}

// insertColumnCells returns cells with an empty field inserted at 0-based index p,
// clamped to the slice bounds so an out-of-range p appends (p >= len) or prepends
// (p <= 0). It never mutates the input.
func insertColumnCells(cells []string, p int) []string {
	if p > len(cells) {
		p = len(cells)
	}
	if p < 0 {
		p = 0
	}
	out := make([]string, 0, len(cells)+1)
	out = append(out, cells[:p]...)
	out = append(out, "")
	return append(out, cells[p:]...)
}

// deleteColumnCells returns cells with the field at 0-based index p removed, and
// whether it removed anything — false when the row has no such column (p out of
// range), the signal to leave that row unchanged. It never mutates the input.
func deleteColumnCells(cells []string, p int) ([]string, bool) {
	if p < 0 || p >= len(cells) {
		return cells, false
	}
	out := make([]string, 0, len(cells)-1)
	out = append(out, cells[:p]...)
	return append(out, cells[p+1:]...), true
}

// rewriteColumns applies fn to every line's parsed cells and joins the result
// back with the delimiter, in two passes so a structural edit is all-or-nothing:
// the first pass parses and computes new lines, aborting with an error if any line
// won't parse (an unbalanced quote with quotes on) so the buffer is never left
// half-edited; only then does it commit. fn returns the new cells and whether it
// changed the row (false leaves the row as is). It returns the map of changed
// lines (0-based index to prior text) for the undo entry, and ok=false on a parse
// abort. With quotes off every line splits, so the abort path bites only quotes-on
// data with a malformed field.
func (r *repl) rewriteColumns(fn func([]string) ([]string, bool)) (changed map[int]string, ok bool) {
	newLines := make([]string, len(r.b.lines))
	changed = map[int]string{}
	for i, line := range r.b.lines {
		cells, pok := rawCells(line, r.delim, r.quotes)
		if !pok {
			emitf("columns: line %d has an unbalanced quote — fix it or try quotes off / dsv off\n", i+1)
			r.last = nil
			return nil, false
		}
		nc, did := fn(cells)
		joined := line
		if did {
			joined = strings.Join(nc, string(r.delim))
		}
		if joined != line {
			changed[i] = line
		}
		newLines[i] = joined
	}
	if len(changed) > 0 {
		r.b.lines = newLines
	}
	return changed, true
}

// commitColumnEdit records the one undo entry that reverses a whole column op —
// restoring every changed line and the modified flag in a single Ctrl+U, like
// replace all — then reprints the block with the letter ruler so the new layout is
// visible. The reprint stays on the block already on screen (r.last) rather than
// jumping to the first changed line, since a column op touches the whole buffer
// and the user is usually looking at a particular spot.
func (r *repl) commitColumnEdit(changed map[int]string, msg string) {
	if len(changed) == 0 {
		emit("columns: no change\n")
		r.last = nil
		return
	}
	first := len(r.b.lines)
	for i := range changed {
		if i < first {
			first = i
		}
	}
	preMod := r.b.modified
	r.b.modified = true
	r.b.pushUndo(undoEntry{
		apply: func(b *buffer) {
			for i, prev := range changed {
				b.lines[i] = prev
			}
			b.modified = preMod
		},
		line: first,
		col:  0,
	})
	emit(faint(msg) + "\n")
	r.printColumns(r.reprintStart(), len(r.b.lines))
}

// reportColumns is the bare columns / c command: reprint the current block with
// the letter ruler above the grid, so the column letters are visible to address
// with insert / kill. The ruler is the column state report — its last letter marks
// the column count — so it doubles as the bare-reports-state convention.
func (r *repl) reportColumns() {
	if r.delim == 0 {
		emit("columns: not in delimited view — set a delimiter first (dsv C)\n")
		r.last = nil
		return
	}
	r.printColumns(r.reprintStart(), len(r.b.lines))
}

// reprintStart is the top line for a columns reprint: the block already on screen
// when there is one, so the view holds still, otherwise the top of the buffer.
func (r *repl) reprintStart() int {
	if r.last != nil {
		return r.last.start
	}
	return 1
}

// columnCount is the number of columns the widest record defines — the maximum
// field count across the buffer. Unparseable lines are skipped (the edit path
// reports them); it sizes a blank record and validates a kill target.
func (r *repl) columnCount() int {
	max := 0
	for _, line := range r.b.lines {
		cells, ok := rawCells(line, r.delim, r.quotes)
		if ok && len(cells) > max {
			max = len(cells)
		}
	}
	return max
}

// headerName is the raw text of column n (1-based) on buffer line 1, used to name
// a column in the kill confirm when headers are on. It returns "" when line 1
// won't parse or has no such column.
func (r *repl) headerName(n int) string {
	cells, ok := rawCells(r.b.lines[0], r.delim, r.quotes)
	if !ok || n < 1 || n > len(cells) {
		return ""
	}
	return cells[n-1]
}

// --- rows ------------------------------------------------------------------

// insertRow splices a blank record into the buffer. A bare arg appends it after
// the last line; a number N inserts it after line N (so N=0 prepends). In a
// delimited view the blank is a well-formed empty record — one empty field per
// column — so it aligns and you can Tab across it; in plain text it is an empty
// line. It reprints from the new line so you can climb in and fill it.
func (r *repl) insertRow(arg string) {
	n := len(r.b.lines)
	pos := n // 0-based index to insert AT; after the last line == append
	if arg != "" {
		m, err := strconv.Atoi(arg)
		if err != nil || m < 0 {
			emitf("rows: insert takes a line number (0 to prepend), not %q\n", arg)
			r.last = nil
			return
		}
		pos = clamp(m, 0, n) // "after line m" is the 0-based index m
	}
	blank := r.blankRecord()
	preMod := r.b.modified
	lines := make([]string, 0, n+1)
	lines = append(lines, r.b.lines[:pos]...)
	lines = append(lines, blank)
	lines = append(lines, r.b.lines[pos:]...)
	r.b.lines = lines
	r.b.modified = true
	r.b.pushUndo(undoEntry{
		apply: func(b *buffer) {
			out := make([]string, 0, len(b.lines)-1)
			out = append(out, b.lines[:pos]...)
			out = append(out, b.lines[pos+1:]...)
			b.lines = out
			b.modified = preMod
		},
		line:      pos,
		col:       0,
		lineDelta: -1, // undo removes the inserted line
	})
	emit(faint(fmt.Sprintf("rows: inserted a row at line %d", pos+1)) + "\n")
	r.reprintFrom(pos + 1)
}

// killRow deletes a line or an inclusive range, addressed the same way the print
// commands are (N, N.M, $, $-k) so a range delete reuses parseAddress. A single
// line goes without ceremony; a range confirms first, since it is the more
// destructive form. The buffer always keeps at least one line, so killing every
// line is refused. The removed lines are captured for a one-step undo.
func (r *repl) killRow(arg string) {
	n := len(r.b.lines)
	if arg == "" {
		emit("rows: kill needs a line number or range — type rows for the count\n")
		r.last = nil
		return
	}
	start, end, ok := parseAddress(arg, n)
	if !ok {
		emitf("rows: kill takes a line number or range, not %q\n", arg)
		r.last = nil
		return
	}
	count := end - start + 1
	if count >= n {
		emitf("rows: can't kill all %d lines — the buffer keeps at least one\n", n)
		r.last = nil
		return
	}
	if count > 1 {
		ans, ok := readLine(r.rd, fmt.Sprintf("kill lines %d-%d (%d lines)? [y/N] ", start, end, count))
		if !ok || !isYes(ans) {
			emit("rows: kill cancelled\n")
			r.last = nil
			return
		}
	}
	idx := start - 1
	removed := append([]string(nil), r.b.lines[idx:end]...)
	preMod := r.b.modified
	out := make([]string, 0, n-count)
	out = append(out, r.b.lines[:idx]...)
	out = append(out, r.b.lines[end:]...)
	r.b.lines = out
	r.b.modified = true
	r.b.pushUndo(undoEntry{
		apply: func(b *buffer) {
			re := make([]string, 0, len(b.lines)+count)
			re = append(re, b.lines[:idx]...)
			re = append(re, removed...)
			re = append(re, b.lines[idx:]...)
			b.lines = re
			b.modified = preMod
		},
		line:      idx,
		col:       0,
		lineDelta: count, // undo re-adds the removed lines
	})
	word := "line"
	if count > 1 {
		word = "lines"
	}
	emit(faint(fmt.Sprintf("rows: killed %d %s", count, word)) + "\n")
	r.reprintFrom(start)
}

// blankRecord is the line a row insert adds: an empty line in plain text, or in a
// delimited view one empty field per column (cols-1 bare delimiters) so the new
// record aligns to the grid and its cells are immediately Tab-navigable.
func (r *repl) blankRecord() string {
	if r.delim == 0 {
		return ""
	}
	if cols := r.columnCount(); cols > 1 {
		return strings.Repeat(string(r.delim), cols-1)
	}
	return ""
}

// reportRows is the bare rows command: the record count, the row analog of the
// columns ruler's implicit count. Like the other state reports it clears r.last.
func (r *repl) reportRows() {
	emitf("rows: %d\n", len(r.b.lines))
	r.last = nil
}

// reprintFrom reprints from a line to the end of the buffer (capped to a
// screenful), the view a row edit leaves on screen — the affected line leads the
// block, ready to climb into. It renders aligned when a delimiter is set, raw
// otherwise, the same as any print.
func (r *repl) reprintFrom(line int) {
	r.printLines(clamp(line, 1, len(r.b.lines)), len(r.b.lines))
}

// isYes reports whether a confirm answer is affirmative — y or yes, case- and
// space-insensitive. Anything else (including empty) is a no, so the default is
// always the safe one.
func isYes(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes":
		return true
	}
	return false
}
