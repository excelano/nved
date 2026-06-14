package main

import (
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

// dsvDispatch handles the DSV command family — the field-layer dsv, quotes, and
// headers, plus the record-layer rows — and reports whether s was one of them so
// the main dispatch can fall through to address parsing when it isn't. A bare
// verb reports current state; an argument sets it. Like every command that prints
// below the block, these clear r.last: the report sits between the block and the
// prompt, so a climb key would no longer land on the right rows.
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
	case s == "rows":
		emitf("rows: %s\n", rowsName(r.b.sep()))
	case s == "rows newline":
		r.setRows('\n')
	case s == "rows record":
		r.setRows('\x1e')
	case strings.HasPrefix(s, "rows "):
		emitf("nved: rows takes newline or record\n")
	case s == "csv":
		r.preset(',', true, true)
	case s == "tsv":
		r.preset('\t', true, true)
	case s == "asv":
		// The one cross-layer preset: record rows first (a buffer reload), then
		// the field layer paints unit-delimited columns on top.
		r.setRows('\x1e')
		r.preset('\x1f', false, true)
	default:
		return false
	}
	r.last = nil
	return true
}

// preset sets the whole field layer at once and reports the resulting mode.
// csv/tsv/asv are nothing but presets over the dsv/quotes/headers primitives —
// not a separate mode — so any one knob can be tuned afterward.
func (r *repl) preset(delim rune, quotes, headers bool) {
	r.delim, r.quotes, r.headers = delim, quotes, headers
	emitf("%s\n", r.dsvState())
}

// setRows switches the record separator. When it actually changes, the buffer is
// re-lined under the new separator — a reload, not an edit: the undo stack is
// cleared (the surrounding dsvDispatch clears the on-screen block). A no-op
// switch to the current separator just reports.
func (r *repl) setRows(sep rune) {
	if r.b.sep() == sep {
		emitf("rows: %s\n", rowsName(sep))
		return
	}
	r.b.reline(sep)
	emitf("rows: %s — buffer re-lined, undo cleared\n", rowsName(sep))
}

// rowsName is the human name of a record separator for the state report.
func rowsName(sep rune) string {
	if sep == '\x1e' {
		return "record separator (0x1E)"
	}
	return "newline"
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

// fieldSpan locates one field inside a raw buffer line: rawStart/rawEnd are rune
// indices bounding the field's raw text — including the surrounding quotes for a
// quoted field — and value is the decoded cell content (quotes stripped, ""
// un-escaped). nved renders the raw text, never the decoded value, so that a
// quoted field reads as quoted on screen; value is kept for round-trip-correct
// save, which is where decoding belongs. The cursor is a raw rune index, which
// the span constrains to a field's interior.
type fieldSpan struct {
	rawStart, rawEnd int
	value            string
	quoted           bool
}

// fieldSpans parses a raw line into its fields, each with the raw rune range it
// occupies and its decoded value. With quotes off it is a naive split on the
// delimiter (every field unquoted, range = the value's runes). With quotes on it
// scans quote-aware: a delimiter inside quotes is part of the field, and a ""
// pair decodes to one quote in value. ok is false on an unbalanced quote — a
// field whose value runs onto the next buffer line — or on a quoted field
// followed by stray text, so callers fall back to the raw view
// rather than trust a malformed parse. It targets well-formed CSV/TSV/DSV; it
// does not chase every encoding/csv leniency on malformed input.
func fieldSpans(line string, delim rune, quotes bool) (spans []fieldSpan, ok bool) {
	rs := []rune(line)
	n := len(rs)
	if !quotes {
		start := 0
		for i := 0; i <= n; i++ {
			if i == n || rs[i] == delim {
				spans = append(spans, fieldSpan{start, i, string(rs[start:i]), false})
				start = i + 1
			}
		}
		return spans, true
	}
	i := 0
	for {
		start := i
		var val strings.Builder
		quoted := i < n && rs[i] == '"'
		if quoted {
			i++ // step over the opening quote
			closed := false
			for i < n {
				if rs[i] == '"' {
					if i+1 < n && rs[i+1] == '"' { // "" escapes one literal quote
						val.WriteRune('"')
						i += 2
						continue
					}
					i++ // the closing quote
					closed = true
					break
				}
				val.WriteRune(rs[i])
				i++
			}
			if !closed {
				return nil, false // a quote that runs off the end is a multi-line field
			}
			if i < n && rs[i] != delim {
				return nil, false // stray text after a closed quote — malformed
			}
		} else {
			for i < n && rs[i] != delim {
				val.WriteRune(rs[i])
				i++
			}
		}
		spans = append(spans, fieldSpan{start, i, val.String(), quoted})
		if i < n && rs[i] == delim {
			i++
			if i == n { // a trailing delimiter leaves one empty final field
				spans = append(spans, fieldSpan{n, n, "", false})
				break
			}
			continue
		}
		break
	}
	return spans, true
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

// rawCells returns each field's raw text — the substring between delimiters, with
// any surrounding quotes kept — which is what nved renders in both the print and
// the climbed-in view, so the two are identical and a quoted field reads as
// quoted. It shares the fieldSpans parse, so display, column widths, and cursor
// navigation all agree on where every field begins. ok is false on an unparseable
// line (an unbalanced quote), the signal to fall back to a raw, unaligned view.
func rawCells(line string, delim rune, quotes bool) ([]string, bool) {
	spans, ok := fieldSpans(line, delim, quotes)
	if !ok {
		return nil, false
	}
	rs := []rune(line)
	cells := make([]string, len(spans))
	for i, s := range spans {
		cells[i] = string(rs[s.rawStart:s.rawEnd])
	}
	return cells, true
}

// fieldOf reports which field a raw cursor index cx sits in. Field boundaries are
// the delimiters: a position belongs to the field whose raw range it falls within,
// and the position just past a delimiter belongs to the next field — so stepping
// the cursor across a delimiter is an ordinary cx+1, with no position ever landing
// "on" the delimiter character itself.
func fieldOf(spans []fieldSpan, cx int) int {
	for f, s := range spans {
		if cx <= s.rawEnd {
			return f
		}
	}
	return len(spans) - 1
}

// alignedVisualCol is the visualCol analog for aligned columns: the on-screen
// column (0-based, past the gutter) of a raw cursor index cx. It sums the padded
// width of every column before the cursor's field — each cell padded to its block
// column width plus the two-space gap — then adds the cursor's offset inside its
// own field. The cell renders as its raw text, so that offset is just cx minus the
// field's raw start; this mirrors alignRow exactly, the way visualCol mirrors
// expandTabs, keeping the cursor on the character it sits under.
func alignedVisualCol(spans []fieldSpan, colW []int, cx int) int {
	f := fieldOf(spans, cx)
	col := 0
	for g := 0; g < f && g < len(colW); g++ {
		col += colW[g] + 2
	}
	if off := cx - spans[f].rawStart; off > 0 {
		col += off
	}
	return col
}

// printBlockAligned renders [start,end] as aligned columns: the faint status row,
// an optional pinned-and-faint header (buffer line 1, when it has scrolled off),
// then each row padded to the block's column grid and truncated at the right edge
// with a faint ›. If any line won't parse (a multi-line quoted field), it bails to
// a raw view with a notice — it never paints a grid it can't stand behind, and
// returns false so the caller marks the block un-alignable (not climbable).
func (r *repl) printBlockAligned(start, end int) bool {
	w := r.gutterW()
	avail := r.termW - (w + 2)
	if avail < 1 {
		avail = 1
	}

	rows := make([][]string, 0, end-start+1)
	for i := start; i <= end; i++ {
		cells, ok := rawCells(r.b.lines[i-1], r.delim, r.quotes)
		if !ok {
			r.printBlockRawNotice(start, end)
			return false
		}
		rows = append(rows, cells)
	}

	showSticky := r.headers && start > 1
	var headerFields []string
	if showSticky {
		hf, ok := rawCells(r.b.lines[0], r.delim, r.quotes)
		if !ok {
			r.printBlockRawNotice(start, end)
			return false
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
	return true
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
