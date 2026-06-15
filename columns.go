package main

import (
	"fmt"
	"strconv"
	"strings"
)

// The structural column layer: insert and kill whole columns across every
// record. Unlike find/replace, which edit one line, a column op rewrites every
// buffer line — it splits each into raw cells, adds or removes one, and rejoins
// verbatim with the delimiter, so a quoted field round-trips exactly (the same
// verbatim join the save path relies on). It is dsv-only: columns exist only once
// a delimiter is set. Columns carry no on-screen address — lines have a gutter
// number, columns don't — so they are named by 1-based position, surfaced on
// demand by the `columns` index ruler. No chords in v1 (every mnemonic key was
// taken: Insert is eaten as Copy, Ctrl+I *is* Tab); the commands are the whole
// surface.

// columnsDispatch handles the column family — bare columns / c prints the index
// ruler over the current block, insert / ci adds an empty column, kill / ck
// removes one — and reports whether s was a column command so the main dispatch
// falls through to address parsing when it isn't. It manages r.last itself: a
// report or a successful edit reprints a climbable block, an error clears it. The
// short forms ci / ck are their own tokens (like fn / rn), matched before the
// long verbs so `c` doesn't swallow them.
func (r *repl) columnsDispatch(s string) bool {
	if rest, ok := cutVerb(s, "ci"); ok {
		r.insertColumn(strings.TrimSpace(rest))
		return true
	}
	if rest, ok := cutVerb(s, "ck"); ok {
		r.killColumn(strings.TrimSpace(rest))
		return true
	}
	rest, ok := cutVerb(s, "columns", "c")
	if !ok {
		return false
	}
	arg := strings.TrimSpace(rest)
	if arg == "" {
		r.reportColumns()
		return true
	}
	verb, rem := arg, ""
	if i := strings.IndexByte(arg, ' '); i >= 0 {
		verb, rem = arg[:i], strings.TrimSpace(arg[i+1:])
	}
	switch verb {
	case "insert":
		r.insertColumn(rem)
	case "kill":
		r.killColumn(rem)
	default:
		emit("nved: columns takes insert or kill — type h for help\n")
		r.last = nil
	}
	return true
}

// insertColumn adds one empty column. A bare arg appends it at the far right; a
// number N inserts it to the right of column N (so N=0 prepends), matching the
// "right of here" mental model the chord would have had. Every parseable row
// gains a cell; a short ragged row gets it at its own end. The buffer line count
// is unchanged — only columns move — so there is no lineDelta.
func (r *repl) insertColumn(arg string) {
	if r.delim == 0 {
		emit("columns: not in delimited view — set a delimiter first (dsv C)\n")
		r.last = nil
		return
	}
	appendEnd := arg == ""
	pos := 0
	if !appendEnd {
		n, err := strconv.Atoi(arg)
		if err != nil || n < 0 {
			emitf("columns: insert takes a column number (0 to prepend), not %q\n", arg)
			r.last = nil
			return
		}
		pos = n // "right of column n" is the 0-based insert index n
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
		msg = fmt.Sprintf("columns: inserted a column right of %d", pos)
	}
	r.commitColumnEdit(changed, msg)
}

// killColumn removes column N from every row. N is required and 1-based; a bare
// kill errors, since the op is destructive across the whole buffer and must name
// its target. It confirms first — naming the column by header when headers are on
// — and a row that has no column N is left untouched.
func (r *repl) killColumn(arg string) {
	if r.delim == 0 {
		emit("columns: not in delimited view — set a delimiter first (dsv C)\n")
		r.last = nil
		return
	}
	if arg == "" {
		emit("columns: kill needs a column number — type columns to see them\n")
		r.last = nil
		return
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 {
		emitf("columns: kill takes a column number (1 or more), not %q\n", arg)
		r.last = nil
		return
	}
	if max := r.columnCount(); n > max {
		emitf("columns: no column %d — there are %d\n", n, max)
		r.last = nil
		return
	}
	label := ""
	if r.headers {
		if name := r.headerName(n); name != "" {
			label = fmt.Sprintf(" %q", name)
		}
	}
	ans, ok := readLine(r.rd, fmt.Sprintf("kill column %d%s in every row? [y/N] ", n, label))
	if !ok || !isYes(ans) {
		emit("columns: kill cancelled\n")
		r.last = nil
		return
	}
	changed, pok := r.rewriteColumns(func(cells []string) ([]string, bool) {
		return deleteColumnCells(cells, n-1)
	})
	if !pok {
		return
	}
	r.commitColumnEdit(changed, fmt.Sprintf("columns: killed column %d", n))
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
// replace all — then reprints the block with the index ruler so the new layout is
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
// the index ruler above the grid, so the 1-based column numbers are visible to
// address with insert / kill. The ruler is the column state report — its highest
// number is the column count — so it doubles as the bare-reports-state convention.
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
// reports them); it is used to validate a kill target before prompting.
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
