package main

import (
	"encoding/csv"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The field layer: how a buffer line becomes columns. dsv sets the delimiter
// (the master switch), quotes and headers are independent toggles. All three are
// view-level — they change how a block renders, never the bytes in the buffer.
// Step 1 wires up the state and the commands; the aligned renderer that reads
// this state lands in step 2.

// dsvDispatch handles the field-layer commands — dsv, quotes, headers — and
// reports whether s was one of them so the main dispatch can fall through to
// address parsing when it isn't. A bare verb reports current state; an argument
// sets it. Like every command that prints below the block, these clear r.last:
// the report sits between the block and the prompt, so a climb key would no
// longer land on the right rows.
func (r *repl) dsvDispatch(s string) bool {
	switch {
	case s == "dsv":
		emitf("%s\n", r.dsvState())
	case strings.HasPrefix(s, "dsv "):
		r.setDelim(strings.TrimPrefix(s, "dsv "))
	case s == "quotes":
		emitf("quotes: %s\n", onOff(r.quotes))
	case strings.HasPrefix(s, "quotes "):
		onOffArg("quotes", strings.TrimPrefix(s, "quotes "), &r.quotes)
	case s == "headers":
		emitf("headers: %s\n", onOff(r.headers))
	case strings.HasPrefix(s, "headers "):
		onOffArg("headers", strings.TrimPrefix(s, "headers "), &r.headers)
	default:
		return false
	}
	r.last = nil
	return true
}

// setDelim applies a dsv argument: "off" clears the master switch back to plain
// text, otherwise parseDelim turns the argument into a delimiter rune. Either
// way it reports the resulting field-layer state, so the user sees the full mode
// they're now in (delimiter, quotes, headers) after one command.
func (r *repl) setDelim(arg string) {
	if arg == "off" {
		r.delim = 0
		emitf("%s\n", r.dsvState())
		return
	}
	d, err := parseDelim(arg)
	if err != nil {
		emitf("nved: %v\n", err)
		return
	}
	r.delim = d
	emitf("%s\n", r.dsvState())
}

// parseDelim turns a dsv argument into a delimiter rune. The grammar is two
// shapes only: a single literal printable character (",", ";", "|"), or a name —
// "tab" (0x09) or "unit" (the ASCII unit separator, 0x1F). There is deliberately
// no escape syntax: "\t" is rejected with a nudge toward "tab", and there is no
// "\xNN" form or names for the other ASCII separators, so a delimiter that
// shouldn't be one can't be set. "off" is handled by the caller.
func parseDelim(arg string) (rune, error) {
	switch arg {
	case "tab":
		return '\t', nil
	case "unit":
		return '\x1f', nil
	case `\t`:
		return 0, fmt.Errorf(`did you mean "dsv tab"?`)
	}
	if utf8.RuneCountInString(arg) == 1 {
		d, _ := utf8.DecodeRuneInString(arg)
		if unicode.IsPrint(d) {
			return d, nil
		}
	}
	return 0, fmt.Errorf("dsv takes one character or a name (tab, unit), not %q", arg)
}

// dsvState is the one-line report for a bare dsv: the full field-layer mode.
// With no delimiter set it says so plainly; otherwise it names the delimiter
// (spelling out an invisible one) and the two toggles, so the report doubles as
// a reminder of what every knob is currently doing.
func (r *repl) dsvState() string {
	if r.delim == 0 {
		return "delimiter: off — plain text"
	}
	return fmt.Sprintf("delimiter: %s, quotes %s, headers %s",
		delimName(r.delim), onOff(r.quotes), onOff(r.headers))
}

// delimName is the human name of a delimiter rune for the state report: the two
// named separators spell themselves out, a printable literal is shown quoted so
// a comma reads as ',' rather than colliding with the report's own commas.
func delimName(d rune) string {
	switch d {
	case '\t':
		return "tab"
	case '\x1f':
		return "unit separator (0x1F)"
	default:
		return fmt.Sprintf("%q", d)
	}
}

// onOffArg sets a bool toggle from an on/off argument, reporting the new state
// or an error that names the command. Shared by quotes and headers.
func onOffArg(label, arg string, field *bool) {
	switch arg {
	case "on", "off":
		*field = arg == "on"
		emitf("%s: %s\n", label, onOff(*field))
	default:
		emitf("nved: %s takes on or off\n", label)
	}
}

// onOff renders a toggle for the state reports.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// --- aligned rendering -----------------------------------------------------
//
// The display half: parse a block's lines into fields, size each column to the
// widest cell, and print rows padded to that grid. This is the command-line
// print path only — the editor still renders raw, so a delimiter set makes the
// printed view aligned-and-read-only (climb in via dsv off). Phase 2 brings the
// cursor math that lets the editor render aligned too.

// splitFields parses one buffer line into its fields. With quotes off it is a
// plain split on the delimiter. With quotes on it goes through encoding/csv so
// "a,b" reads as one field; a line that won't parse — an unbalanced quote, i.e.
// a field whose value continues onto the next buffer line — returns ok=false,
// the signal to fall back to a raw view rather than render a misleading grid.
func splitFields(line string, delim rune, quotes bool) (fields []string, ok bool) {
	if !quotes {
		return strings.Split(line, string(delim)), true
	}
	if line == "" {
		return []string{""}, true // csv reads a blank line as EOF; treat as one empty field
	}
	rd := csv.NewReader(strings.NewReader(line))
	rd.Comma = delim
	rd.FieldsPerRecord = -1 // ragged rows are allowed
	rec, err := rd.Read()
	if err != nil {
		return nil, false
	}
	return rec, true
}

// colWidths sizes each column to its widest cell across the given rows. The
// vector's length is the maximum field count, so ragged rows leave later columns
// sized by whatever rows do reach them. Widths are block-scoped: the caller
// passes only the rows on screen (plus the header line), keeping a print O(block).
func colWidths(rows [][]string) []int {
	cols := 0
	for _, row := range rows {
		if len(row) > cols {
			cols = len(row)
		}
	}
	w := make([]int, cols)
	for _, row := range rows {
		for f, cell := range row {
			if d := dispWidth(cell); d > w[f] {
				w[f] = d
			}
		}
	}
	return w
}

// alignRow pads each field to its column width and joins with a two-space gap —
// the expandTabs analog for DSV. Two spaces over box-drawing pipes: quieter,
// ASCII-safe, and it doesn't compete with the faint gutter. The final column is
// not padded, so a full-width row carries no trailing whitespace (a ragged short
// row ends in the empty cells it lacks, which are invisible on screen anyway).
func alignRow(fields []string, w []int) string {
	var b strings.Builder
	for f := 0; f < len(w); f++ {
		if f > 0 {
			b.WriteString("  ")
		}
		var cell string
		if f < len(fields) {
			cell = fields[f]
		}
		b.WriteString(cell)
		if f < len(w)-1 {
			if pad := w[f] - dispWidth(cell); pad > 0 {
				b.WriteString(strings.Repeat(" ", pad))
			}
		}
	}
	return b.String()
}

// dispWidth is a cell's width in display columns. Like visualCol it counts each
// rune as one column (double-width CJK included), the same simplification the
// rest of nved's cursor math makes; acceptable for the narrow files this targets.
func dispWidth(s string) int { return utf8.RuneCountInString(s) }

// truncateDisplay cuts s to at most max display columns, reporting whether it had
// to. When it cuts it leaves one column for the caller's overflow marker.
func truncateDisplay(s string, max int) (string, bool) {
	if max < 1 {
		max = 1
	}
	if dispWidth(s) <= max {
		return s, false
	}
	return string([]rune(s)[:max-1]), true
}

// printBlockAligned renders [start,end] as aligned columns: the faint status row,
// an optional pinned-and-faint header (buffer line 1, when it has scrolled off),
// then each row padded to the block's column grid and truncated at the right edge
// with a faint ›. If any line won't parse (a multi-line quoted field), it bails to
// a raw view with a notice — it never paints a grid it can't stand behind.
func (r *repl) printBlockAligned(start, end int) {
	w := r.gutterW()
	avail := r.termW - (w + 2)
	if avail < 1 {
		avail = 1
	}

	rows := make([][]string, 0, end-start+1)
	for i := start; i <= end; i++ {
		fields, ok := splitFields(r.b.lines[i-1], r.delim, r.quotes)
		if !ok {
			r.printBlockRawNotice(start, end)
			return
		}
		rows = append(rows, fields)
	}

	showSticky := r.headers && start > 1
	var headerFields []string
	if showSticky {
		hf, ok := splitFields(r.b.lines[0], r.delim, r.quotes)
		if !ok {
			r.printBlockRawNotice(start, end)
			return
		}
		headerFields = hf
	}

	// Size columns over the block plus the header line, so the pinned header
	// aligns to the same grid as the body.
	wRows := rows
	if showSticky {
		wRows = append([][]string{headerFields}, rows...)
	}
	colW := colWidths(wRows)

	out("\r" + csiEL + r.header(start, end) + "\r\n")
	if showSticky {
		emitAlignedRow(w, 1, alignRow(headerFields, colW), avail, true)
	}
	for k, fields := range rows {
		num := start + k
		// Buffer line 1 is the column header — drawn faint whether it sits at the
		// top of the block (start == 1) or pinned above it, so it reads the same way.
		dim := r.headers && num == 1
		emitAlignedRow(w, num, alignRow(fields, colW), avail, dim)
	}
}

// emitAlignedRow prints one gutter-prefixed aligned row, truncated at the right
// edge with a › marker when it overflows. A dim row (the column header) is drawn
// entirely faint — number, text, and marker; an ordinary row fainted only in its
// gutter and overflow marker, like every other printed line.
func emitAlignedRow(w, num int, aligned string, avail int, dim bool) {
	text, cut := truncateDisplay(aligned, avail)
	var row string
	if dim {
		row = fmt.Sprintf("%*d  ", w, num) + text
		if cut {
			row += "›"
		}
		row = faint(row)
	} else {
		row = gutterPrefix(w, num) + text
		if cut {
			row += faint("›")
		}
	}
	out("\r" + csiEL + row + "\r\n")
}

// printBlockRawNotice prints a one-line reason and then the block as plain text —
// the graceful degrade when a block can't be aligned honestly.
func (r *repl) printBlockRawNotice(start, end int) {
	out("\r" + csiEL + faint("dsv: unbalanced quote — multi-line field, showing raw") + "\r\n")
	r.printBlockRaw(start, end)
}
