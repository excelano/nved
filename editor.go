package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"unicode"
)

// screen is where the editor's raw bytes go. It is a variable so tests can
// redirect it to io.Discard instead of spraying escape codes at the terminal.
var screen io.Writer = os.Stdout

// out writes raw bytes to the terminal. Unlike emit it does NOT translate "\n"
// to "\r\n" — the editor emits cursor-control escapes and explicit "\r\n" pairs
// itself, and emit's rewrite would corrupt them.
func out(s string) { io.WriteString(screen, s) }

// ANSI controls. CUU/CUD move up/down n rows; CHA sets the absolute column
// (1-based); EL erases from the cursor to the end of the line; ED (csiED) erases
// from the cursor to the end of the screen. csiWrapOff/csiWrapOn disable and
// re-enable the terminal's own line wrap: the editor wraps long lines itself, so
// it turns the terminal's wrap off and emits its own "\r\n" breaks, which keeps
// the cursor math free of the terminal's deferred-wrap ("phantom column")
// quirk.
const (
	csiEL      = "\x1b[K"
	csiED      = "\x1b[J"
	csiWrapOff = "\x1b[?7l"
	csiWrapOn  = "\x1b[?7h"
	csiHide    = "\x1b[?25l" // hide the cursor while a redraw walks it across rows
	csiShow    = "\x1b[?25h" // show it again once it is back at rest
)

func cuu(n int) string {
	if n <= 0 {
		return ""
	}
	return "\x1b[" + strconv.Itoa(n) + "A"
}

func cud(n int) string {
	if n <= 0 {
		return ""
	}
	return "\x1b[" + strconv.Itoa(n) + "B"
}

func cha(col int) string { return "\x1b[" + strconv.Itoa(col) + "G" }

// editor is one editing session over the most-recently-printed block. The block
// is the editable universe: edits never reach across its top or bottom edge into
// buffer lines that aren't on screen, which keeps the redraw self-contained and
// the cursor math honest.
//
// The cursor position is tracked logically as (cy, cx) — a 0-based row within
// the block and a 0-based rune index within that row's text. A logical line can
// wrap onto several physical terminal rows, so the mapping from (cy, cx) to a
// physical (row, column) goes through physCursor: it sums the wrapped heights of
// the lines above and then splits the cursor's cell offset by the terminal
// width. Every move is emitted relative to where the cursor already is, computed
// by taking physCursor before and after the change.
type editor struct {
	r     *repl
	start int // 1-based buffer line shown at the top of the block
	count int // number of logical lines the block currently shows
	cy    int // cursor row within the block, 0-based (logical line)
	cx    int // cursor column within the row, 0-based rune index

	// aligned is set when the block renders as DSV columns rather than raw text:
	// every line is one screen row (no wrap), cells pad to colW, and the cursor
	// steps over delimiters. colW is the block's column-width vector, recomputed
	// whenever the block's text changes. When aligned is false the editor is the
	// original raw, word-wrapped editor and colW is unused.
	aligned bool
	colW    []int

	// flash is set while a save confirmation occupies the header row; the next
	// key restores the normal header.
	flash bool
}

// editAction is what an editing session asks run() to do once it closes. Most
// keys keep editing and return nothing (actNone). Page-Up/Page-Down leave and
// page from the command row; Ctrl+X leaves and exits; an unnamed Ctrl+S leaves to
// be prompted for a file name. (A named Ctrl+S saves in place and does NOT leave,
// so it has no action here.) Ctrl+U whose edit is off-screen leaves with actUndo
// so run() can reprint it at the prompt. Routing exit, the unnamed save, and the
// off-screen undo back through run() keeps one dispatch path shared with the
// command line.
type editAction int

const (
	actNone editAction = iota
	actPageUp
	actPageDown
	actSave
	actExit
	actUndo // the undone edit lies outside this block — leave and reprint it
)

// edit runs an editing session, entered by a climb key pressed at the command
// line. It turns the terminal's own wrap off, repaints the block in our
// wrap-aware layout, edits, then restores the prompt and the terminal's wrap. It
// reuses the width printLines drew the block at (r.termW) rather than re-reading
// it, so the editor's layout matches what is already on screen. On return r.last
// reflects the block's (possibly changed) size.
func (r *repl) edit(climb key) editAction {
	e := &editor{r: r, start: r.last.start, count: r.last.count}
	e.aligned = r.lastAligned
	if e.aligned {
		e.recomputeColW()
	}

	switch {
	case climb.kind == keyLeft:
		e.cy, e.cx = e.count-1, e.lineLen(e.count-1) // end of the last line
	case climb.kind == keyHome && climb.ctrl:
		e.cy, e.cx = 0, 0 // Ctrl+Home: first line
	default: // keyUp
		e.cy, e.cx = e.count-1, 0 // bottom line
	}

	// Clear the prompt we're climbing out of and take over wrapping. The prompt
	// sits one physical row below the block, i.e. blockHeight rows below the
	// top, so repaintAll climbs from there, redraws the block in our layout, and
	// lands the cursor at its target.
	out("\r" + csiEL)
	out(csiWrapOff)
	e.repaintAll(e.blockHeight())

	action := e.loop()

	// Drop to the command line directly below the (possibly resized) block,
	// clear it, and hand wrapping back to the terminal.
	curRow, _ := e.physCursor()
	out(cud(e.blockHeight()-curRow) + "\r" + csiEL)
	out(csiWrapOn)
	r.last = &block{start: e.start, count: e.count}
	return action
}

// loop reads and handles keys until the user leaves the editor, returning what
// run() should do next: nothing, or page in a direction (Page-Up/Down leave the
// editor and page from the command line).
func (e *editor) loop() editAction {
	for {
		k, ok := e.r.rd.readKey()
		if !ok {
			return actNone
		}
		// A save confirmation flashed in the header is cleared on the next key,
		// restoring the live line counts.
		if e.flash {
			e.paintHeader(e.r.header(e.start, e.start+e.count-1))
			e.flash = false
		}
		switch k.kind {
		case keyCtrlC, keyEsc:
			return actNone
		case keyCtrlX:
			return actExit
		case keyCtrlS:
			if e.r.b.name == "" {
				return actSave // no file yet — drop to the prompt to name it
			}
			e.save()
		case keyLeft:
			switch {
			case k.ctrl && e.aligned:
				e.cellLeft()
			case k.ctrl:
				e.wordLeft()
			default:
				e.moveLeft()
			}
		case keyRight:
			switch {
			case k.ctrl && e.aligned:
				e.cellRight()
			case k.ctrl:
				e.wordRight()
			case e.cx < e.lineLen(e.cy):
				e.moveTo(e.cy, e.cx+1)
			case e.cy < e.count-1:
				e.moveTo(e.cy+1, 0) // wrap to start of next line
			default:
				return actNone // right past the last character leaves, mirroring Left entering there
			}
		case keyUp:
			if e.cy > 0 {
				e.moveTo(e.cy-1, e.cx)
			}
		case keyDown:
			if e.cy < e.count-1 {
				e.moveTo(e.cy+1, e.cx)
			} else {
				return actNone // down past the last line leaves, mirroring Up entering there
			}
		case keyPageUp:
			// Leave and page up, but only when there's a line above the block to
			// reach; otherwise ignore the key and stay, like Up at the top row.
			if e.start > 1 {
				return actPageUp
			}
		case keyPageDown:
			if e.start+e.count <= len(e.r.b.lines) {
				return actPageDown
			}
		case keyHome:
			if k.ctrl {
				e.moveTo(0, 0)
			} else {
				e.moveTo(e.cy, 0)
			}
		case keyEnd:
			if k.ctrl {
				e.moveTo(e.count-1, e.lineLen(e.count-1))
			} else {
				e.moveTo(e.cy, e.lineLen(e.cy))
			}
		case keyEnter:
			if !e.aligned { // splitting a row is structural — raw mode only
				e.splitLine()
			}
		case keyBackspace:
			e.backspace() // aligned: no-op at a row edge (guarded inside)
		case keyDelete:
			e.del()
		case keyCtrlU:
			if act := e.undo(); act != actNone {
				return act
			}
		case keyTab:
			if e.aligned {
				e.cellRight()
			} else {
				e.insert('\t')
			}
		case keyShiftTab:
			if e.aligned {
				e.cellLeft()
			}
		case keyRune:
			if e.aligned && k.r == e.r.delim && !e.inQuotedValue() {
				break // outside quotes the delimiter would add a column — structural, raw mode only
			}
			e.insert(k.r)
		}
	}
}

// --- geometry helpers ------------------------------------------------------

func (e *editor) lineIdx() int           { return e.start - 1 + e.cy }
func (e *editor) lineText(cy int) string { return e.r.b.lines[e.start-1+cy] }
func (e *editor) curLine() string        { return e.lineText(e.cy) }
func (e *editor) lineLen(cy int) int     { return len([]rune(e.lineText(cy))) }

// tw is the terminal width to wrap at, with a sane fallback so tests and a
// failed GetSize never divide by zero.
func (e *editor) tw() int {
	if e.r.termW <= 0 {
		return 80
	}
	return e.r.termW
}

// width is the gutter width, sized to the whole buffer's largest line number so
// columns stay put regardless of which range is on screen — the same rule the
// command-line printer uses.
func (e *editor) width() int { return len(strconv.Itoa(len(e.r.b.lines))) }

// availWidth is the number of columns left for text on a row once the gutter (or
// the equal-width continuation marker) is subtracted — the wrap width A. It is
// the same for the first and continuation rows, so wrapping is uniform.
func (e *editor) availWidth() int {
	a := e.tw() - (e.width() + 2)
	if a < 1 {
		a = 1
	}
	return a
}

// disp is the tab-expanded display runes of line cy — the units that wrap.
func (e *editor) disp(cy int) []rune { return []rune(expandTabs(e.lineText(cy))) }

// physHeightOf is how many physical rows line cy occupies once word-wrapped — or
// exactly one in aligned mode, where rows never wrap, which is what collapses the
// whole vertical half of the cursor math (lineTopRow becomes cy, blockHeight the
// line count).
func (e *editor) physHeightOf(cy int) int {
	if e.aligned {
		return 1
	}
	return len(wrapRows(e.disp(cy), e.availWidth()))
}

// sticky reports whether a pinned column-header row floats above the block — only
// in aligned mode, with headers on, when line 1 has scrolled off the top. It adds
// one display row above the block that the editor must redraw and climb past.
func (e *editor) sticky() bool { return e.aligned && e.r.headers && e.start > 1 }

// headRows is how many rows sit above the block top that the editor manages: the
// faint status row always, plus the pinned column header when sticky.
func (e *editor) headRows() int {
	if e.sticky() {
		return 2
	}
	return 1
}

// inQuotedValue reports whether the cursor sits inside the quoted interior of its
// field — past the opening quote and before the closing one. There the delimiter
// is ordinary data (the comma in "a,b"), so typing it is a value edit, not a new
// column. Everywhere else in aligned mode the delimiter stays suppressed: with
// quotes off no field is ever quoted, and at a quote's own position the delimiter
// would land outside it and split the row. To put a delimiter into an unquoted
// cell, quote the cell first (type " at each end), then type inside.
func (e *editor) inQuotedValue() bool {
	if !e.r.quotes {
		return false
	}
	spans := e.alignedSpans(e.curLine())
	s := spans[fieldOf(spans, e.cx)]
	return s.quoted && e.cx > s.rawStart && e.cx < s.rawEnd
}

// alignedSpans is the editor's parse of one line: fieldSpans, but a line that
// won't parse — a quote left transiently unbalanced mid-edit — falls back to a
// single span covering the whole line instead of failing. That keeps the cursor
// math and the render alive while you type the second quote: the line shows as one
// raw cell until it balances, never crashing and never losing the text.
func (e *editor) alignedSpans(text string) []fieldSpan {
	if spans, ok := fieldSpans(text, e.r.delim, e.r.quotes); ok {
		return spans
	}
	return []fieldSpan{{0, len([]rune(text)), text, false}}
}

// alignedCells is the raw cell text of one line, robust to an unparseable line the
// same way alignedSpans is — what the aligned editor renders and measures.
func (e *editor) alignedCells(text string) []string {
	spans := e.alignedSpans(text)
	rs := []rune(text)
	cells := make([]string, len(spans))
	for i, s := range spans {
		cells[i] = string(rs[s.rawStart:s.rawEnd])
	}
	return cells
}

// recomputeColW sizes the aligned columns over the whole block — plus the header
// line when it is pinned, so the floating header shares the body's grid. Widths
// are over the raw cell text, the same text the aligned editor renders and edits.
func (e *editor) recomputeColW() {
	rows := make([][]string, 0, e.count+1)
	if e.sticky() {
		rows = append(rows, e.alignedCells(e.r.b.lines[0]))
	}
	for j := 0; j < e.count; j++ {
		rows = append(rows, e.alignedCells(e.lineText(j)))
	}
	e.colW = colWidths(rows)
}

// lineTopRow is the physical row offset, from the top of the block, of the first
// row of logical line cy.
func (e *editor) lineTopRow(cy int) int {
	h := 0
	for j := 0; j < cy; j++ {
		h += e.physHeightOf(j)
	}
	return h
}

// blockHeight is the total physical height of the block once every line is
// wrapped.
func (e *editor) blockHeight() int { return e.lineTopRow(e.count) }

// physCursor maps the logical cursor (cy, cx) to its physical position: the row
// offset from the top of the block and the 1-based terminal column. It wraps the
// cursor's line, finds which wrapped row the cursor's visual column lands on, and
// offsets the column past the gutter. A cursor sitting just off the end of a row
// (past the wrap width, e.g. on a hanging break space) is clamped to the last
// terminal column.
func (e *editor) physCursor() (rowOff, chaCol int) {
	if e.aligned {
		// One row per line, so the row offset is just cy; the column comes from the
		// aligned visualCol analog, which maps the raw cursor index across padded
		// columns. An unparseable line can't happen here — the block is alignable.
		spans := e.alignedSpans(e.curLine())
		chaCol = e.width() + 2 + alignedVisualCol(spans, e.colW, e.cx) + 1
		if chaCol > e.tw() {
			chaCol = e.tw()
		}
		return e.cy, chaCol
	}
	base := e.lineTopRow(e.cy)
	starts := wrapRows(e.disp(e.cy), e.availWidth())
	r, col := rowOf(starts, visualCol(e.curLine(), e.cx))
	chaCol = e.width() + 2 + col + 1
	if chaCol > e.tw() {
		chaCol = e.tw()
	}
	return base + r, chaCol
}

// --- movement --------------------------------------------------------------

// moveTo repositions the cursor to (cy, cx), clamping into range. No text
// changes, so physCursor computed before and after the change agree on the
// physical layout; the difference gives the relative row move, then an absolute
// column set.
func (e *editor) moveTo(cy, cx int) {
	cy = clamp(cy, 0, e.count-1)
	cx = clamp(cx, 0, e.lineLen(cy))
	oldRow, _ := e.physCursor()
	e.cy, e.cx = cy, cx
	newRow, newCol := e.physCursor()
	out(cud(newRow - oldRow))
	out(cuu(oldRow - newRow))
	out(cha(newCol))
}

func (e *editor) moveLeft() {
	switch {
	case e.cx > 0:
		e.moveTo(e.cy, e.cx-1)
	case e.cy > 0:
		e.moveTo(e.cy-1, e.lineLen(e.cy-1)) // wrap to end of previous line
	}
}

// wordLeft moves the cursor to the start of the previous word — a navigation aid
// that skips by words instead of characters. At the start of a line it steps to
// the end of the previous line, mirroring moveLeft's line wrap.
func (e *editor) wordLeft() {
	t := prevWordStart([]rune(e.curLine()), e.cx)
	if t == e.cx && e.cy > 0 {
		e.moveTo(e.cy-1, e.lineLen(e.cy-1))
		return
	}
	e.moveTo(e.cy, t)
}

// wordRight moves the cursor to the start of the next word. At the end of a line
// it steps to the start of the next line. Unlike a plain Right past the last
// character it never leaves the editor — word skipping is navigation only.
func (e *editor) wordRight() {
	t := nextWordStart([]rune(e.curLine()), e.cx)
	if t == e.cx && e.cy < e.count-1 {
		e.moveTo(e.cy+1, 0)
		return
	}
	e.moveTo(e.cy, t)
}

// cellRight jumps the cursor to the start of the next cell — the aligned-mode
// reading of "skip by the meaningful unit", bound to both Ctrl+Right and Tab (Tab
// can't insert a literal tab here: in a TSV that tab is the delimiter). From the
// last cell it wraps to the start of the next row, mirroring wordRight at a line
// end; navigation only, it never leaves the editor.
func (e *editor) cellRight() {
	spans := e.alignedSpans(e.curLine())
	f := fieldOf(spans, e.cx)
	if f < len(spans)-1 {
		e.moveTo(e.cy, spans[f+1].rawStart)
		return
	}
	if e.cy < e.count-1 {
		e.moveTo(e.cy+1, 0)
	}
}

// cellLeft is the mirror, bound to Ctrl+Left and Shift-Tab: to the start of the
// current cell if the cursor is partway into it, otherwise to the start of the
// previous cell, wrapping to the end of the previous row at the first cell — the
// same shape as wordLeft.
func (e *editor) cellLeft() {
	spans := e.alignedSpans(e.curLine())
	f := fieldOf(spans, e.cx)
	switch {
	case e.cx > spans[f].rawStart:
		e.moveTo(e.cy, spans[f].rawStart)
	case f > 0:
		e.moveTo(e.cy, spans[f-1].rawStart)
	case e.cy > 0:
		e.moveTo(e.cy-1, e.lineLen(e.cy-1))
	}
}

// prevWordStart returns the index of the start of the word at or before i: it
// skips any whitespace immediately to the left, then the word characters before
// that. At i == 0 it returns 0, so the caller can tell movement stalled.
func prevWordStart(rs []rune, i int) int {
	for i > 0 && unicode.IsSpace(rs[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(rs[i-1]) {
		i--
	}
	return i
}

// nextWordStart returns the index of the start of the next word after i: it
// skips the rest of the current word, then the whitespace following it. At the
// end of the slice it returns len(rs), so the caller can tell movement stalled.
func nextWordStart(rs []rune, i int) int {
	n := len(rs)
	for i < n && !unicode.IsSpace(rs[i]) {
		i++
	}
	for i < n && unicode.IsSpace(rs[i]) {
		i++
	}
	return i
}

// --- editing ---------------------------------------------------------------
//
// Each mutator changes the buffer, marks it modified, updates the cursor,
// records an inverse on the buffer's undo stack, and repaints. They are the
// single funnel every edit flows through. Before mutating they capture the
// cursor's physical row (prePhysRow), because after the edit the terminal cursor
// is still where it was while (cy, cx) already point at the new spot — the
// repaint needs the old physical row to climb back to the block. The inverse
// captures absolute buffer indices (idx is already one) plus the modified flag
// and the line/column the edit began at, so Ctrl+U restores the text and lands
// where the edit began whether the block is still on screen or not.

// finishCharEdit redraws after an in-line character edit. If the line's wrapped
// height is unchanged, only that line is rewritten in place; otherwise the lines
// below have shifted, so the whole block is repainted.
func (e *editor) finishCharEdit(prePhysRow, preH int) {
	if e.aligned {
		// A cell edit can change its column's width, which reflows every later
		// column on every row — a full repaint with a moving cursor. Recompute the
		// widths and compare: unchanged means only this one row's text moved, so the
		// fast single-line redraw still holds.
		old := e.colW
		e.recomputeColW()
		if sameWidths(old, e.colW) {
			e.redrawLine(prePhysRow)
		} else {
			e.repaintAll(prePhysRow)
		}
		return
	}
	if e.physHeightOf(e.cy) == preH {
		e.redrawLine(prePhysRow)
	} else {
		e.repaintAll(prePhysRow)
	}
}

// sameWidths reports whether two column-width vectors are identical — the test for
// whether a cell edit reflowed the grid or stayed within its column.
func sameWidths(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// insert places a rune at the cursor and advances past it.
func (e *editor) insert(r rune) {
	idx := e.lineIdx()
	preCx, preMod := e.cx, e.r.b.modified
	prePhysRow, _ := e.physCursor()
	preH := e.physHeightOf(e.cy)
	rs := []rune(e.r.b.lines[idx])
	e.r.b.lines[idx] = string(spliceRune(rs, e.cx, r))
	e.r.b.modified = true
	e.cx++
	e.r.b.pushUndo(undoEntry{
		apply: func(b *buffer) {
			b.lines[idx] = string(deleteRune([]rune(b.lines[idx]), preCx))
			b.modified = preMod
		},
		line: idx, col: preCx,
	})
	e.finishCharEdit(prePhysRow, preH)
}

// splitLine breaks the current line at the cursor, dropping the tail to a new
// line below and landing the cursor at its start.
func (e *editor) splitLine() {
	idx := e.lineIdx()
	preCx, preMod := e.cx, e.r.b.modified
	prePhysRow, _ := e.physCursor()
	rs := []rune(e.curLine())
	left, right := string(rs[:e.cx]), string(rs[e.cx:])
	e.r.b.lines = insertLine(e.r.b.lines, idx+1, right)
	e.r.b.lines[idx] = left
	e.r.b.modified = true
	e.count++
	e.cy, e.cx = e.cy+1, 0
	e.r.b.pushUndo(undoEntry{
		apply: func(b *buffer) {
			b.lines[idx] += b.lines[idx+1]
			b.lines = removeLine(b.lines, idx+1)
			b.modified = preMod
		},
		line: idx, col: preCx, lineDelta: -1,
	})
	e.repaintAll(prePhysRow)
}

// backspace deletes the rune before the cursor, or — at the start of a line —
// joins the line onto the end of the previous one. It will not join across the
// top of the block.
func (e *editor) backspace() {
	idx := e.lineIdx()
	if e.aligned && e.cx == 0 {
		return // joining rows is a structural edit — do it in raw mode (dsv off)
	}
	if e.cx > 0 {
		preCx, preMod := e.cx, e.r.b.modified
		prePhysRow, _ := e.physCursor()
		preH := e.physHeightOf(e.cy)
		rs := []rune(e.r.b.lines[idx])
		gone := rs[e.cx-1]
		e.r.b.lines[idx] = string(deleteRune(rs, e.cx-1))
		e.r.b.modified = true
		e.cx--
		e.r.b.pushUndo(undoEntry{
			apply: func(b *buffer) {
				b.lines[idx] = string(spliceRune([]rune(b.lines[idx]), preCx-1, gone))
				b.modified = preMod
			},
			line: idx, col: preCx,
		})
		e.finishCharEdit(prePhysRow, preH)
		return
	}
	if e.cy == 0 {
		return // can't join past the top of the block
	}
	preMod := e.r.b.modified
	prePhysRow, _ := e.physCursor()
	joinAt := e.lineLen(e.cy - 1)
	e.r.b.lines[idx-1] += e.r.b.lines[idx]
	e.r.b.lines = removeLine(e.r.b.lines, idx)
	e.r.b.modified = true
	e.count--
	e.cy, e.cx = e.cy-1, joinAt
	e.r.b.pushUndo(undoEntry{
		apply: func(b *buffer) {
			rs := []rune(b.lines[idx-1])
			b.lines = insertLine(b.lines, idx, string(rs[joinAt:]))
			b.lines[idx-1] = string(rs[:joinAt])
			b.modified = preMod
		},
		line: idx, col: 0, lineDelta: 1,
	})
	e.repaintAll(prePhysRow)
}

// del deletes the rune under the cursor, or — at the end of a line — pulls the
// next line up onto this one. It will not join across the bottom of the block.
func (e *editor) del() {
	idx := e.lineIdx()
	rs := []rune(e.curLine())
	if e.aligned && e.cx >= len(rs) {
		return // pulling up the next row is a structural edit — raw mode only
	}
	if e.cx < len(rs) {
		preCx, preMod := e.cx, e.r.b.modified
		prePhysRow, _ := e.physCursor()
		preH := e.physHeightOf(e.cy)
		gone := rs[e.cx]
		e.r.b.lines[idx] = string(deleteRune(rs, e.cx))
		e.r.b.modified = true
		e.r.b.pushUndo(undoEntry{
			apply: func(b *buffer) {
				b.lines[idx] = string(spliceRune([]rune(b.lines[idx]), preCx, gone))
				b.modified = preMod
			},
			line: idx, col: preCx,
		})
		e.finishCharEdit(prePhysRow, preH)
		return
	}
	if e.cy == e.count-1 {
		return // can't pull from below the block
	}
	preCx, preMod := e.cx, e.r.b.modified // preCx == len(rs), the join point
	prePhysRow, _ := e.physCursor()
	e.r.b.lines[idx] += e.r.b.lines[idx+1]
	e.r.b.lines = removeLine(e.r.b.lines, idx+1)
	e.r.b.modified = true
	e.count--
	e.r.b.pushUndo(undoEntry{
		apply: func(b *buffer) {
			rs := []rune(b.lines[idx])
			b.lines = insertLine(b.lines, idx+1, string(rs[preCx:]))
			b.lines[idx] = string(rs[:preCx])
			b.modified = preMod
		},
		line: idx, col: preCx, lineDelta: 1,
	})
	e.repaintAll(prePhysRow)
}

// undo reverses the most recent edit. With the stack empty it does nothing
// (actNone). When the edit lies inside the block currently on screen — the
// common case, undoing what you just typed — it applies the inverse, resizes the
// block by the edit's line delta, restores the cursor, and repaints in place. A
// join-undo re-creates a line just past the block's bottom, so the in-block test
// allows for that growth. When the edit lies elsewhere (the stack outlived the
// block it was made in) it returns actUndo without popping, leaving the editor so
// run() can reprint the edit at the prompt — undo always reverses the last edit,
// wherever it was.
func (e *editor) undo() editAction {
	entry, ok := e.r.b.peekUndo()
	if !ok {
		return actNone
	}
	lo := e.start - 1
	hi := e.start - 1 + e.count - 1
	if entry.lineDelta > 0 {
		hi += entry.lineDelta
	}
	if entry.line < lo || entry.line > hi {
		return actUndo
	}
	e.r.b.popUndo()
	prePhysRow, _ := e.physCursor()
	entry.apply(e.r.b)
	e.count += entry.lineDelta
	e.cy = entry.line - (e.start - 1)
	e.cx = entry.col
	if e.aligned { // the restored text may re-size columns
		e.recomputeColW()
	}
	e.repaintAll(prePhysRow)
	return actNone
}

// save writes the buffer to disk without leaving the editor — Ctrl+S checkpoints
// in place — and flashes a confirmation in the header row, leaving the cursor
// where it is. It is only reached with a named buffer; an unnamed Ctrl+S returns
// actSave so run() can prompt for a name at the command line.
func (e *editor) save() {
	n, err := e.r.b.save()
	if err != nil {
		e.paintHeader(faint(fmt.Sprintf("nved: %v", err)))
	} else {
		e.paintHeader(faint(fmt.Sprintf("wrote %d lines to %s", n, e.r.b.name)))
	}
	e.flash = true
}

// paintHeader rewrites the faint header row above the block in place and returns
// the cursor to where it was, so a message can replace the header without
// repainting the block. The header sits one physical row above the block top —
// curRow+1 rows above the cursor.
func (e *editor) paintHeader(text string) {
	curRow, curCol := e.physCursor()
	out(csiHide)
	out(cuu(curRow+e.headRows()) + "\r" + csiEL + text)
	out(cud(curRow+e.headRows()) + cha(curCol))
	out(csiShow)
}

// --- redraw ----------------------------------------------------------------

// emitLine prints one logical buffer line as its physical rows, each row cleared
// first and rows separated by CRLF, ending with a CRLF so the cursor lands at
// the start of the row just below the printed line. The first row carries the
// line-number gutter, continuation rows the arrow marker; the
// text is word-wrapped to A columns by the same wrapRows the cursor math uses,
// so the row count always equals physHeightOf.
func emitLine(w, num int, text string, A int) {
	disp := []rune(expandTabs(text))
	starts := wrapRows(disp, A)
	for r, s := range starts {
		end := len(disp)
		if r+1 < len(starts) {
			end = starts[r+1]
		}
		prefix := contPrefix(w)
		if r == 0 {
			prefix = gutterPrefix(w, num)
		}
		out("\r" + csiEL + prefix + string(disp[s:end]) + "\r\n")
	}
}

// redrawLine rewrites just the current logical line in place — the fast path for
// a character edit that did not change the line's wrapped height, so the lines
// below it have not moved. prePhysRow is where the cursor physically sat before
// the edit; it climbs from there to the line's top, rewrites the line's rows,
// then drops to the cursor's new position.
func (e *editor) redrawLine(prePhysRow int) {
	lineTop := e.lineTopRow(e.cy)
	out(csiHide)
	out(cuu(prePhysRow - lineTop)) // up to the first row of this line
	if e.aligned {
		e.emitAligned(e.start+e.cy, e.curLine())
	} else {
		emitLine(e.width(), e.start+e.cy, e.curLine(), e.availWidth())
	}
	afterRow := lineTop + e.physHeightOf(e.cy)
	targetRow, targetCol := e.physCursor()
	out(cuu(afterRow-targetRow) + cha(targetCol))
	out(csiShow)
}

// repaintAll repaints the whole block after a structural or height-changing
// edit. prePhysRow is the cursor's physical row before the edit; it climbs from
// there past the block's top to the faint header row one line above it, rewrites
// the header (so the line counts stay live as edits add or remove lines) and
// every wrapped line, erases anything a now-shorter block left stranded below,
// then drops to the cursor. Because the rewrite ends with the cursor physically
// below the block, the final upward move is immune to any scroll it triggered.
func (e *editor) repaintAll(prePhysRow int) {
	out(csiHide)
	out(cuu(prePhysRow+e.headRows()) + "\r") // up past the block to the status row
	out(csiEL + e.r.header(e.start, e.start+e.count-1) + "\r\n")
	if e.sticky() { // the pinned column header, redrawn so it tracks the grid
		e.emitAligned(1, e.r.b.lines[0])
	}
	for j := 0; j < e.count; j++ {
		if e.aligned {
			e.emitAligned(e.start+j, e.lineText(j))
		} else {
			emitLine(e.width(), e.start+j, e.lineText(j), e.availWidth())
		}
	}
	out(csiED) // erase the rows a shrink would otherwise strand
	targetRow, targetCol := e.physCursor()
	out(cuu(e.blockHeight()-targetRow) + cha(targetCol))
	out(csiShow)
}

// emitAligned prints one aligned row — buffer line num, rendered as its raw cells
// padded to the block grid — for the climbed-in DSV view. Line 1 is drawn faint
// as the column header, matching the command-line print path's emitAlignedRow.
func (e *editor) emitAligned(num int, text string) {
	dim := e.r.headers && num == 1
	emitAlignedRow(e.width(), num, alignRow(e.alignedCells(text), e.colW), e.availWidth(), dim)
}

// --- rune-slice surgery ----------------------------------------------------
// Each builds a fresh slice rather than appending in place, to dodge the
// aliasing trap where append(s[:i], s[i:]...) overwrites the tail mid-copy.

func spliceRune(rs []rune, i int, r rune) []rune {
	out := make([]rune, 0, len(rs)+1)
	out = append(out, rs[:i]...)
	out = append(out, r)
	return append(out, rs[i:]...)
}

func deleteRune(rs []rune, i int) []rune {
	out := make([]rune, 0, len(rs)-1)
	out = append(out, rs[:i]...)
	return append(out, rs[i+1:]...)
}

func insertLine(lines []string, i int, s string) []string {
	lines = append(lines, "")
	copy(lines[i+1:], lines[i:])
	lines[i] = s
	return lines
}

func removeLine(lines []string, i int) []string {
	copy(lines[i:], lines[i+1:])
	return lines[:len(lines)-1]
}
