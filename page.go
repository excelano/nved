package main

import (
	"fmt"
	"strconv"
)

// A print is capped to one screenful (see printLines): you see the top of the
// range and Page-Up/Page-Down reprint the neighbouring screenful to walk through
// the rest, each new page scrolling the old one into history. None of this owns
// the screen — every page is just another printed block, so terminal scrollback
// still accumulates as you page and the cursor only ever has to reach lines that
// are on screen.

// gutterW is the line-number gutter width, sized to the whole buffer's largest
// line number so a line renders at a fixed column no matter what range is shown.
func (r *repl) gutterW() int { return len(strconv.Itoa(len(r.b.lines))) }

// textWidth is the columns left for text once the gutter (gutterW+2) is removed —
// the wrap width A, the same value the editor wraps at.
func (r *repl) textWidth() int {
	a := r.termW - (r.gutterW() + 2)
	if a < 1 {
		a = 1
	}
	return a
}

// availRows is the physical rows of text content a page may hold: the terminal
// height less two reserved rows — one for the faint header at the top, one for
// the prompt at the bottom. A full page is therefore header + availRows content +
// prompt = termH rows, which scrolls the previous page entirely off and pins the
// header to the top row. The content block still lands directly above the prompt,
// so the climb-in math (cursor up blockHeight rows) reaches the block's top while
// the header, sitting above it, is left untouched.
func (r *repl) availRows() int {
	a := r.termH - 2
	if r.delim != 0 && r.headers {
		a-- // reserve a row for the pinned data header
	}
	if a < 1 {
		a = 1
	}
	return a
}

// header is the faint status row printed above a block: which lines are showing,
// and — when there is somewhere to page — a reminder of the paging keys. It is
// drawn in the same SGR-faint as the line-number gutter, a brightness cue that
// survives mild colour-blindness.
func (r *repl) header(start, end int) string {
	n := len(r.b.lines)
	s := fmt.Sprintf("lines %d-%d of %d", start, end, n)
	if start > 1 || end < n {
		s += " · Page-Up/Page-Down to navigate"
	}
	return faint(s)
}

// oneRowPerLine reports whether the current view renders each buffer line as
// exactly one screen row, panning a too-wide row sideways rather than wrapping it:
// the aligned DSV view (delimiter set) and plain text with wrap off. It collapses
// the whole vertical paging math to a simple line count and tells the print and
// editor paths to window rows horizontally instead of wrapping them.
func (r *repl) oneRowPerLine() bool { return r.delim != 0 || !r.wrap }

// physHeight is how many physical rows buffer line i (1-based) occupies once
// word-wrapped at the current width — or exactly one when the view is
// one-row-per-line (aligned DSV, or wrap off), which collapses the vertical paging
// math (visibleTail, fillDown, the page windows) to a simple line count.
func (r *repl) physHeight(i int) int {
	if r.oneRowPerLine() {
		return 1
	}
	return len(wrapRows([]rune(expandTabs(r.b.lines[i-1])), r.textWidth()))
}

// visibleTail returns the topmost line of the on-screen tail after printing the
// inclusive range [start,end]: walking up from end, it keeps the lines whose
// wrapped heights fit one screenful, always including end itself even if that
// last line alone overflows. Lines above the result scrolled off and aren't
// climbable. When the whole range fits, it returns start unchanged.
func (r *repl) visibleTail(start, end int) int {
	avail := r.availRows()
	used, top := 0, end
	for i := end; i >= start; i-- {
		h := r.physHeight(i)
		if i < end && used+h > avail {
			break
		}
		used += h
		top = i
	}
	return top
}

// fillDown returns the bottommost line b such that [start,b] fills at most one
// screenful, always including start. It is the forward mirror of visibleTail,
// used to size a Page-Down.
func (r *repl) fillDown(start int) int {
	avail := r.availRows()
	used, bot := 0, start
	n := len(r.b.lines)
	for i := start; i <= n; i++ {
		h := r.physHeight(i)
		if i > start && used+h > avail {
			break
		}
		used += h
		bot = i
	}
	return bot
}

// pageDownWindow returns the line range a Page-Down should reprint, given the
// current block's top and length, or ok=false at the end of the buffer. The page
// starts just below the block and fills a screenful. If it reaches the last line
// it re-anchors to the bottom (the maximal screenful ending at line n) so the
// page stays full and scrolls the previous one entirely off — a short page would
// strand the bottom of the previous one above it.
func (r *repl) pageDownWindow(blockStart, blockCount int) (start, end int, ok bool) {
	n := len(r.b.lines)
	start = blockStart + blockCount
	if start > n {
		return 0, 0, false
	}
	end = r.fillDown(start)
	if end == n {
		start = r.visibleTail(1, n)
	}
	return start, end, true
}

// pageUpWindow is the mirror: the page ends just above the block and fills a
// screenful upward, re-anchoring to line 1 (a full screenful from the top) when
// it reaches the start of the buffer.
func (r *repl) pageUpWindow(blockStart int) (start, end int, ok bool) {
	end = blockStart - 1
	if end < 1 {
		return 0, 0, false
	}
	start = r.visibleTail(1, end)
	if start == 1 {
		end = r.fillDown(1)
	}
	return start, end, true
}

// pageDown reprints the screenful below the current block, scrolling it into view
// so those lines become climbable. At the end of the buffer it does nothing.
func (r *repl) pageDown() {
	if r.last == nil {
		return
	}
	r.refreshSize()
	if start, end, ok := r.pageDownWindow(r.last.start, r.last.count); ok {
		r.printLines(start, end)
	}
}

// pageUp reprints the screenful above the current block. At the top of the buffer
// it does nothing.
func (r *repl) pageUp() {
	if r.last == nil {
		return
	}
	r.refreshSize()
	if start, end, ok := r.pageUpWindow(r.last.start); ok {
		r.printLines(start, end)
	}
}
