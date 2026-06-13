package main

import (
	"fmt"
	"strings"
)

// tabWidth is the column span of a tab stop. Tabs are measured from the start of
// the text (the first character after the gutter), not from the absolute
// terminal column, so the cursor math and the on-screen rendering agree no
// matter how wide the line-number gutter is. The buffer keeps literal tabs; only
// the display expands them.
const tabWidth = 8

// faint wraps s in the SGR faint attribute (ESC[2m … ESC[22m). The line-number
// gutter is drawn faint so it reads as dimmer than the body text — a brightness
// cue rather than a colour one, so it survives mild colour-blindness.
func faint(s string) string { return "\x1b[2m" + s + "\x1b[22m" }

// gutterPrefix is the first-row prefix: the right-aligned, faint line number and
// two spaces. Its visible width is w+2; the SGR codes occupy no columns.
func gutterPrefix(w, num int) string { return faint(fmt.Sprintf("%*d", w, num)) + "  " }

// contPrefix is the continuation-row prefix: blank space the same width as
// gutterPrefix (w+2), so wrapped text stays column-aligned with the first row
// while the empty gutter quietly sets continuation rows apart.
func contPrefix(w int) string { return strings.Repeat(" ", w+2) }

// expandTabs replaces each tab with spaces up to the next tab stop, counting
// columns from the start of the text. visualCol must mirror this accounting
// exactly or the cursor will drift away from the characters it sits on.
func expandTabs(s string) string {
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			n := tabWidth - col%tabWidth
			b.WriteString(strings.Repeat(" ", n))
			col += n
		} else {
			b.WriteRune(r)
			col++
		}
	}
	return b.String()
}

// visualCol returns the on-screen column (0-based, relative to the start of the
// text) of the cursor sitting before rune index cx — i.e. its index into the
// tab-expanded display string. Every rune counts as width one except a tab,
// which advances to the next tab stop, mirroring expandTabs. Double-width runes
// (CJK) are treated as width one; that gap is acceptable for narrow files.
func visualCol(s string, cx int) int {
	col, i := 0, 0
	for _, r := range s {
		if i >= cx {
			break
		}
		if r == '\t' {
			col += tabWidth - col%tabWidth
		} else {
			col++
		}
		i++
	}
	return col
}

// wrapRows word-wraps the expanded display runes into physical rows no wider
// than A columns and returns the starting index of each row: row r spans
// [starts[r], starts[r+1]), the last row running to the end. It breaks after the
// last space that fits, so whole words stay together; a word longer than A is
// hard-broken. A break space is kept at the end of the row it follows (it may
// hang one or more columns past A), which keeps every rune assigned to exactly
// one row so the cursor mapping stays unambiguous. A is forced to at least 1.
func wrapRows(disp []rune, A int) []int {
	if A < 1 {
		A = 1
	}
	starts := []int{0}
	rowStart := 0
	lastBreak := -1 // index just after the most recent space in this row
	for i := 0; i < len(disp); i++ {
		if disp[i] != ' ' && i-rowStart >= A {
			if lastBreak > rowStart {
				rowStart = lastBreak
			} else {
				rowStart = i // no break point: hard-break mid-word
			}
			starts = append(starts, rowStart)
			lastBreak = -1
		}
		if disp[i] == ' ' {
			lastBreak = i + 1
		}
	}
	return starts
}

// rowOf finds which physical row the visual column vc falls on, given the row
// starts from wrapRows, and the column within that row. A column exactly at a
// row boundary belongs to the later row (the head of the continuation), so the
// cursor lands at the start of the wrapped row rather than off the end of the
// previous one.
func rowOf(starts []int, vc int) (row, col int) {
	r := 0
	for r+1 < len(starts) && starts[r+1] <= vc {
		r++
	}
	return r, vc - starts[r]
}
