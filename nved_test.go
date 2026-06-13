package main

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// newEditor builds an editor over a one-block buffer with the terminal output
// discarded, so a test can drive the real mutators and undo without escape
// codes reaching the terminal.
func newEditor(t *testing.T, lines []string, cy, cx int) *editor {
	t.Helper()
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	b := &buffer{lines: append([]string(nil), lines...)}
	return &editor{r: &repl{b: b, termW: 80}, start: 1, count: len(lines), cy: cy, cx: cx}
}

func TestSplitLines(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", []string{""}},     // empty file -> one empty line
		{"a", []string{"a"}},   // no trailing newline
		{"a\n", []string{"a"}}, // trailing newline dropped
		{"a\nb", []string{"a", "b"}},
		{"a\nb\n", []string{"a", "b"}},
		{"\n", []string{""}},         // a lone newline -> one empty line
		{"a\n\n", []string{"a", ""}}, // intentional trailing blank line kept
	}
	for _, c := range cases {
		got := splitLines([]byte(c.in))
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitLines(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseAddress(t *testing.T) {
	cases := []struct {
		in           string
		n            int
		wStart, wEnd int
		ok           bool
	}{
		{"5", 10, 5, 5, true},
		{"1,3", 10, 1, 3, true},
		{",", 10, 1, 10, true},
		{"1,$", 10, 1, 10, true},
		{"$", 10, 10, 10, true},
		{"10", 5, 5, 5, true},   // clamp single high
		{"0", 5, 1, 1, true},    // clamp single low
		{"1,10", 5, 1, 5, true}, // clamp range end
		{"5,2", 10, 2, 5, true}, // reversed range swapped
		{",$", 10, 1, 10, true},
		{"3,", 10, 3, 10, true},
		{"$,$", 4, 4, 4, true},
		{"abc", 10, 0, 0, false},
		{"1,x", 10, 0, 0, false},
	}
	for _, c := range cases {
		gs, ge, ok := parseAddress(c.in, c.n)
		if ok != c.ok || (ok && (gs != c.wStart || ge != c.wEnd)) {
			t.Errorf("parseAddress(%q, %d) = (%d,%d,%v), want (%d,%d,%v)",
				c.in, c.n, gs, ge, ok, c.wStart, c.wEnd, c.ok)
		}
	}
}

func TestSaveOpenRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.txt")
	want := []string{"one", "two", "three"}
	b := &buffer{name: p, lines: want, modified: true}
	if _, err := b.save(); err != nil {
		t.Fatal(err)
	}
	if b.modified {
		t.Error("save should clear the modified flag")
	}
	b2, newFile, err := openBuffer(p)
	if err != nil {
		t.Fatal(err)
	}
	if newFile {
		t.Error("existing file reported as new")
	}
	if !reflect.DeepEqual(b2.lines, want) {
		t.Errorf("round-trip lines = %q, want %q", b2.lines, want)
	}
}

func TestOpenMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nope.txt")
	b, newFile, err := openBuffer(p)
	if err != nil {
		t.Fatal(err)
	}
	if !newFile {
		t.Error("missing file should report new")
	}
	if !reflect.DeepEqual(b.lines, []string{""}) {
		t.Errorf("new-file buffer = %q, want one empty line", b.lines)
	}
	if b.name != p {
		t.Errorf("new-file buffer should remember name %q, got %q", p, b.name)
	}
}

func TestOpenUnnamed(t *testing.T) {
	b, newFile, err := openBuffer("")
	if err != nil {
		t.Fatal(err)
	}
	if newFile || b.name != "" || !reflect.DeepEqual(b.lines, []string{""}) {
		t.Errorf("unnamed buffer = %+v, want one empty line, no name", b)
	}
}

func TestExitWarnTwice(t *testing.T) {
	b := &buffer{modified: true}
	if doExit(b) {
		t.Fatal("dirty buffer should not exit on first x")
	}
	if !b.exitArmed {
		t.Fatal("first x on a dirty buffer should arm the warning")
	}
	if !doExit(b) {
		t.Fatal("dirty buffer should exit on the second x")
	}
}

func TestExitCleanImmediate(t *testing.T) {
	b := &buffer{modified: false}
	if !doExit(b) {
		t.Fatal("clean buffer should exit immediately")
	}
}

func TestInterveningCommandDisarmsExit(t *testing.T) {
	b := &buffer{modified: true, lines: []string{""}}
	doExit(b)                  // arm
	(&repl{b: b}).dispatch("") // an empty command line should disarm
	if b.exitArmed {
		t.Fatal("an intervening command should disarm the exit warning")
	}
}

func TestExpandTabsAndVisualCol(t *testing.T) {
	cases := []struct {
		in       string
		want     string // tab-expanded display
		cx, wCol int    // visual column before rune index cx
	}{
		{"ab", "ab", 2, 2},
		{"\tx", "        x", 1, 8},        // a leading tab fills to column 8
		{"a\tb", "a       b", 2, 8},       // tab from column 1 still lands on 8
		{"abcdefg\th", "abcdefg h", 8, 8}, // tab from column 7 advances one
		{"\t\t", "                ", 2, 16},
	}
	for _, c := range cases {
		if got := expandTabs(c.in); got != c.want {
			t.Errorf("expandTabs(%q) = %q, want %q", c.in, got, c.want)
		}
		if got := visualCol(c.in, c.cx); got != c.wCol {
			t.Errorf("visualCol(%q, %d) = %d, want %d", c.in, c.cx, got, c.wCol)
		}
	}
}

func TestWrapRows(t *testing.T) {
	cases := []struct {
		in   string
		A    int
		want []int
	}{
		{"abc", 10, []int{0}},                // fits on one row
		{"hello world foo", 10, []int{0, 6}}, // break after "hello ", "world foo" fits
		{"aa bb cc", 5, []int{0, 6}},         // break after the second space; trailing space hangs
		{"abcdefghij", 4, []int{0, 4, 8}},    // no spaces: hard-break every 4
		{"", 5, []int{0}},                    // empty line still has one row
	}
	for _, c := range cases {
		got := wrapRows([]rune(c.in), c.A)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("wrapRows(%q, %d) = %v, want %v", c.in, c.A, got, c.want)
		}
	}
}

func TestPhysCursor(t *testing.T) {
	// One line in a 1-line buffer: gutter width 1, prefix 3 columns. termW 13
	// leaves A = 10 for text. "hello world foo" wraps to ["hello ", "world foo"].
	e := newEditor(t, []string{"hello world foo"}, 0, 0)
	e.r.termW = 13
	cases := []struct {
		cx, wRow, wCol int
	}{
		{0, 0, 4},   // start of text, just past the 3-column gutter
		{6, 1, 4},   // the 'w' begins the second wrapped row
		{15, 1, 13}, // end of line: last row, last column
	}
	for _, c := range cases {
		e.cx = c.cx
		row, col := e.physCursor()
		if row != c.wRow || col != c.wCol {
			t.Errorf("physCursor cx=%d = (%d,%d), want (%d,%d)", c.cx, row, col, c.wRow, c.wCol)
		}
	}
}

func TestBlockGeometry(t *testing.T) {
	// First line wraps to two rows, second is one row. A = 13 - 3 = 10.
	e := newEditor(t, []string{"hello world foo", "y"}, 0, 0)
	e.r.termW = 13
	if got := e.physHeightOf(0); got != 2 {
		t.Errorf("physHeightOf(0) = %d, want 2", got)
	}
	if got := e.lineTopRow(1); got != 2 {
		t.Errorf("lineTopRow(1) = %d, want 2", got)
	}
	if got := e.blockHeight(); got != 3 {
		t.Errorf("blockHeight() = %d, want 3", got)
	}
}

func newRepl(lines []string, termW, termH int) *repl {
	return &repl{b: &buffer{lines: append([]string(nil), lines...)}, termW: termW, termH: termH}
}

func TestVisibleTailAndFillDown(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "x" // every line one physical row
	}
	r := newRepl(lines, 80, 10) // availRows = termH-2 = 8

	// Printing the whole file scrolls all but the last 8 lines off; the tail
	// starts at line 93.
	if got := r.visibleTail(1, 100); got != 93 {
		t.Errorf("visibleTail(1,100) = %d, want 93", got)
	}
	// A range that already fits is returned whole.
	if got := r.visibleTail(95, 100); got != 95 {
		t.Errorf("visibleTail(95,100) = %d, want 95", got)
	}
	// Page-Down from the top fills eight rows: lines 1..8.
	if got := r.fillDown(1); got != 8 {
		t.Errorf("fillDown(1) = %d, want 8", got)
	}
	// Near the end fillDown stops at the last line, short of a full screenful.
	if got := r.fillDown(96); got != 100 {
		t.Errorf("fillDown(96) = %d, want 100", got)
	}
}

func TestPageWindows(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "x"
	}
	r := newRepl(lines, 80, 10) // availRows = termH-2 = 8

	cases := []struct {
		name         string
		fn           func() (int, int, bool)
		wStart, wEnd int
		wOK          bool
	}{
		// Mid-buffer pages are plain full screenfuls of 8 lines.
		{"down mid", func() (int, int, bool) { return r.pageDownWindow(1, 8) }, 9, 16, true},
		{"up mid", func() (int, int, bool) { return r.pageUpWindow(50) }, 42, 49, true},
		// Page-Down that reaches the end re-anchors to the bottom: a full 8-line
		// screenful ending at 100, not the short 96..100.
		{"down to end", func() (int, int, bool) { return r.pageDownWindow(88, 8) }, 93, 100, true},
		// Page-Up that reaches the top re-anchors to line 1: 1..8, not 1..4.
		{"up to top", func() (int, int, bool) { return r.pageUpWindow(5) }, 1, 8, true},
		// Past the edges there is no page.
		{"down past end", func() (int, int, bool) { return r.pageDownWindow(93, 8) }, 0, 0, false},
		{"up past top", func() (int, int, bool) { return r.pageUpWindow(1) }, 0, 0, false},
	}
	for _, c := range cases {
		s, e, ok := c.fn()
		if s != c.wStart || e != c.wEnd || ok != c.wOK {
			t.Errorf("%s = (%d,%d,%v), want (%d,%d,%v)", c.name, s, e, ok, c.wStart, c.wEnd, c.wOK)
		}
	}
}

func TestPageGeometryWrapped(t *testing.T) {
	// gutterW = 1 (3 lines), textWidth = 13 - 3 = 10. The third line is 25 runes
	// with no spaces, so it hard-wraps to three physical rows. Block heights are
	// [1, 1, 3] = 5 physical rows.
	lines := []string{"x", "y", "aaaaaaaaaaaaaaaaaaaaaaaaa"}
	r := newRepl(lines, 13, 6) // availRows = termH-2 = 4
	if got := r.physHeight(3); got != 3 {
		t.Fatalf("physHeight(3) = %d, want 3", got)
	}
	// 5 rows don't fit in 4, so the top line scrolls off.
	if got := r.visibleTail(1, 3); got != 2 {
		t.Errorf("visibleTail(1,3) = %d, want 2", got)
	}
	// A taller terminal (availRows = 5) fits the whole block.
	r.termH = 7
	if got := r.visibleTail(1, 3); got != 1 {
		t.Errorf("visibleTail(1,3) at height 7 = %d, want 1", got)
	}
}

func TestRuneSurgery(t *testing.T) {
	if got := string(spliceRune([]rune("ac"), 1, 'b')); got != "abc" {
		t.Errorf("spliceRune = %q, want %q", got, "abc")
	}
	if got := string(spliceRune([]rune("bc"), 0, 'a')); got != "abc" {
		t.Errorf("spliceRune at head = %q, want %q", got, "abc")
	}
	if got := string(deleteRune([]rune("abc"), 1)); got != "ac" {
		t.Errorf("deleteRune = %q, want %q", got, "ac")
	}
	// multi-byte runes must be handled by index, not byte offset
	if got := string(spliceRune([]rune("aü"), 1, 'x')); got != "axü" {
		t.Errorf("spliceRune (utf8) = %q, want %q", got, "axü")
	}
}

func TestUndoInsert(t *testing.T) {
	e := newEditor(t, []string{"ac", "world"}, 0, 1)
	e.insert('b')
	if e.curLine() != "abc" || e.cx != 2 || !e.r.b.modified {
		t.Fatalf("after insert: %q cx=%d mod=%v", e.curLine(), e.cx, e.r.b.modified)
	}
	e.undo()
	if e.curLine() != "ac" || e.cy != 0 || e.cx != 1 {
		t.Fatalf("after undo: %q cy=%d cx=%d", e.curLine(), e.cy, e.cx)
	}
	if e.r.b.modified {
		t.Error("undoing the only edit should restore modified=false")
	}
}

func TestUndoSplit(t *testing.T) {
	e := newEditor(t, []string{"helloworld", "x"}, 0, 5)
	e.splitLine()
	if !reflect.DeepEqual(e.r.b.lines, []string{"hello", "world", "x"}) || e.count != 3 {
		t.Fatalf("after split: %q count=%d", e.r.b.lines, e.count)
	}
	if e.cy != 1 || e.cx != 0 {
		t.Fatalf("after split cursor: cy=%d cx=%d, want 1,0", e.cy, e.cx)
	}
	e.undo()
	if !reflect.DeepEqual(e.r.b.lines, []string{"helloworld", "x"}) || e.count != 2 {
		t.Fatalf("after undo: %q count=%d", e.r.b.lines, e.count)
	}
	if e.cy != 0 || e.cx != 5 {
		t.Fatalf("after undo cursor: cy=%d cx=%d, want 0,5", e.cy, e.cx)
	}
}

func TestUndoBackspaceJoin(t *testing.T) {
	e := newEditor(t, []string{"foo", "bar"}, 1, 0)
	e.backspace() // join "bar" onto "foo"
	if !reflect.DeepEqual(e.r.b.lines, []string{"foobar"}) || e.count != 1 {
		t.Fatalf("after join: %q count=%d", e.r.b.lines, e.count)
	}
	if e.cy != 0 || e.cx != 3 {
		t.Fatalf("after join cursor: cy=%d cx=%d, want 0,3", e.cy, e.cx)
	}
	e.undo()
	if !reflect.DeepEqual(e.r.b.lines, []string{"foo", "bar"}) || e.count != 2 {
		t.Fatalf("after undo: %q count=%d", e.r.b.lines, e.count)
	}
	if e.cy != 1 || e.cx != 0 {
		t.Fatalf("after undo cursor: cy=%d cx=%d, want 1,0", e.cy, e.cx)
	}
}

func TestUndoDeleteJoin(t *testing.T) {
	e := newEditor(t, []string{"foo", "bar"}, 0, 3) // cursor at end of "foo"
	e.del()                                         // pull "bar" up
	if !reflect.DeepEqual(e.r.b.lines, []string{"foobar"}) || e.count != 1 {
		t.Fatalf("after del-join: %q count=%d", e.r.b.lines, e.count)
	}
	e.undo()
	if !reflect.DeepEqual(e.r.b.lines, []string{"foo", "bar"}) || e.cy != 0 || e.cx != 3 {
		t.Fatalf("after undo: %q cy=%d cx=%d", e.r.b.lines, e.cy, e.cx)
	}
}

// Strict LIFO: several edits, then undo them in reverse to the original state.
func TestUndoLIFO(t *testing.T) {
	e := newEditor(t, []string{"a", "b"}, 0, 1)
	e.insert('x') // "ax"
	e.splitLine() // "ax" / "" ... cursor on the new empty line
	e.insert('y') // new line becomes "y"
	want := []string{"ax", "y", "b"}
	if !reflect.DeepEqual(e.r.b.lines, want) {
		t.Fatalf("after edits: %q, want %q", e.r.b.lines, want)
	}
	e.undo() // remove the 'y'
	e.undo() // unsplit
	e.undo() // remove the 'x'
	if !reflect.DeepEqual(e.r.b.lines, []string{"a", "b"}) || e.count != 2 {
		t.Fatalf("after full undo: %q count=%d", e.r.b.lines, e.count)
	}
	if e.r.b.modified {
		t.Error("undoing every edit should restore modified=false")
	}
	e.undo() // empty stack: no-op, must not panic
}

func TestLineSurgery(t *testing.T) {
	got := insertLine([]string{"a", "c"}, 1, "b")
	if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("insertLine = %q, want [a b c]", got)
	}
	got = insertLine([]string{"a", "b"}, 2, "c") // append at end
	if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("insertLine at end = %q, want [a b c]", got)
	}
	got = removeLine([]string{"a", "b", "c"}, 1)
	if !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Errorf("removeLine = %q, want [a c]", got)
	}
}

func TestWordBoundaries(t *testing.T) {
	rs := []rune("foo bar  baz")
	// nextWordStart lands on the start of each following word, then end of line.
	for _, c := range []struct{ from, want int }{
		{0, 4},  // "foo" -> "bar"
		{1, 4},  // mid-word -> "bar"
		{4, 9},  // "bar" -> "baz" (skips the double space)
		{9, 12}, // "baz" -> end of line
		{12, 12},
	} {
		if got := nextWordStart(rs, c.from); got != c.want {
			t.Errorf("nextWordStart(%d) = %d, want %d", c.from, got, c.want)
		}
	}
	// prevWordStart lands on the start of the word at or before the index.
	for _, c := range []struct{ from, want int }{
		{12, 9}, // end -> "baz"
		{9, 4},  // "baz" -> "bar"
		{6, 4},  // mid "bar" -> "bar"
		{4, 0},  // "bar" -> "foo"
		{0, 0},
	} {
		if got := prevWordStart(rs, c.from); got != c.want {
			t.Errorf("prevWordStart(%d) = %d, want %d", c.from, got, c.want)
		}
	}
}

func TestSaveArg(t *testing.T) {
	for _, c := range []struct {
		in       string
		wantName string
		wantOK   bool
	}{
		{"s", "", true},
		{"save", "", true},
		{"s notes.txt", "notes.txt", true},
		{"save notes.txt", "notes.txt", true},
		{"s   spaced.txt  ", "spaced.txt", true},
		{"saved", "", false}, // a word that merely starts with "s"
		{"x", "", false},
		{"", "", false},
	} {
		name, ok := saveArg(c.in)
		if name != c.wantName || ok != c.wantOK {
			t.Errorf("saveArg(%q) = (%q, %v), want (%q, %v)", c.in, name, ok, c.wantName, c.wantOK)
		}
	}
}
