package main

import (
	"encoding/csv"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
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
	return &editor{r: &repl{b: b, termW: 80, wrap: true}, start: 1, count: len(lines), cy: cy, cx: cx}
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
		{".", 10, 1, 10, true},        // dot is an alias for the comma: all lines
		{"1.3", 10, 1, 3, true},       // dot range
		{"3.", 10, 3, 10, true},       // dot to the end
		{".5", 10, 1, 5, true},        // start to dot
		{"5.2", 10, 2, 5, true},       // reversed dot range swapped
		{"$-9", 100, 91, 91, true},    // offset from the last line
		{"$-9.$", 100, 91, 100, true}, // last ten lines
		{"$-200", 100, 1, 1, true},    // offset underflow clamps to line 1
		{"$+5", 100, 100, 100, true},  // offset past the end clamps to $
		{"$-20.$-10", 100, 80, 90, true},
		{"$-x", 100, 0, 0, false}, // malformed offset
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

func TestHeadTail(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "x"
	}
	r := newRepl(lines, 80, 10)

	// Count branches are deterministic — no terminal read.
	cases := []struct {
		in           string
		wStart, wEnd int
		ok, matched  bool
	}{
		{"head", 1, 100, true, true},     // bare: whole file, printLines caps it
		{"head 3", 1, 3, true, true},     // first N
		{"head 500", 1, 100, true, true}, // count past the end clamps
		{"tail 10", 91, 100, true, true}, // last N
		{"tail 500", 1, 100, true, true}, // count past the end clamps to line 1
		{"head 0", 0, 0, false, true},    // non-positive count
		{"tail -1", 0, 0, false, true},   // non-positive count
		{"tail abc", 0, 0, false, true},  // malformed count
		{"tailor", 0, 0, false, false},   // not a head/tail command
		{"headphones", 0, 0, false, false},
		{"5.10", 0, 0, false, false},
	}
	for _, c := range cases {
		gs, ge, ok, matched := r.headTail(c.in)
		if matched != c.matched || ok != c.ok || (ok && (gs != c.wStart || ge != c.wEnd)) {
			t.Errorf("headTail(%q) = (%d,%d,ok=%v,matched=%v), want (%d,%d,ok=%v,matched=%v)",
				c.in, gs, ge, ok, matched, c.wStart, c.wEnd, c.ok, c.matched)
		}
	}

	// Bare tail ends at the last line and starts on a valid line; the exact
	// screenful top depends on the live terminal size (see TestVisibleTailAndFillDown).
	gs, ge, ok, matched := r.headTail("tail")
	if !matched || !ok || ge != 100 || gs < 1 || gs > 100 {
		t.Errorf(`headTail("tail") = (%d,%d,ok=%v,matched=%v), want end=100, 1<=start<=100`, gs, ge, ok, matched)
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
	return &repl{b: &buffer{lines: append([]string(nil), lines...)}, termW: termW, termH: termH, wrap: true}
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

// The undo stack lives on the buffer, so an edit made while climbed in is still
// undoable from the command prompt after the editing session is gone.
func TestUndoAtPrompt(t *testing.T) {
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	r := newRepl([]string{"ac", "world"}, 80, 24)
	e := &editor{r: r, start: 1, count: 2, cy: 0, cx: 1}
	e.insert('b') // "abc"
	if r.b.lines[0] != "abc" {
		t.Fatalf("after insert: %q, want abc", r.b.lines[0])
	}
	// Session over; undo now comes from the prompt against the buffer's stack.
	r.undoAtPrompt()
	if r.b.lines[0] != "ac" {
		t.Fatalf("after prompt undo: %q, want ac", r.b.lines[0])
	}
	if r.b.modified {
		t.Error("undoing the only edit should restore modified=false")
	}
	if r.last == nil || r.last.start != 1 || r.last.count != 1 {
		t.Fatalf("prompt undo should reprint the affected line, r.last=%+v", r.last)
	}
	r.undoAtPrompt() // empty stack: reports nothing to undo, must not panic
}

// An edit whose line lies outside the block currently climbed into is not undone
// in place: undo returns actUndo (leaving the entry on the stack) so run() can
// reprint it at the prompt.
func TestUndoOutOfBlockLeaves(t *testing.T) {
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	r := newRepl([]string{"a", "b", "c", "d", "e"}, 80, 24)
	e1 := &editor{r: r, start: 1, count: 1, cy: 0, cx: 0}
	e1.insert('X') // line 1 -> "Xa"
	// Climb into a later block; the edit sits above it.
	e2 := &editor{r: r, start: 4, count: 2, cy: 0, cx: 0}
	if act := e2.undo(); act != actUndo {
		t.Fatalf("out-of-block undo = %v, want actUndo", act)
	}
	if _, ok := r.b.peekUndo(); !ok {
		t.Fatal("out-of-block undo must leave the entry on the stack")
	}
	if r.b.lines[0] != "Xa" {
		t.Fatalf("out-of-block undo must not mutate the buffer yet: %q", r.b.lines[0])
	}
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

func TestParseDelim(t *testing.T) {
	cases := []struct {
		in   string
		want rune
		ok   bool
	}{
		{",", ',', true},
		{";", ';', true},
		{"|", '|', true},
		{"tab", '\t', true},
		{"unit", '\x1f', true},
		{`\t`, 0, false}, // backslash-escape rejected, not interpreted
		{"ab", 0, false}, // more than one character
		{"", 0, false},   // empty
	}
	for _, c := range cases {
		got, err := parseDelim(c.in)
		if (err == nil) != c.ok || (c.ok && got != c.want) {
			t.Errorf("parseDelim(%q) = (%q, err=%v), want (%q, ok=%v)", c.in, got, err, c.want, c.ok)
		}
	}
}

func TestDsvDispatch(t *testing.T) {
	r := newRepl([]string{"a,b,c"}, 80, 24)

	// Arguments set the field-layer state, and every one is matched.
	for _, c := range []struct {
		cmd  string
		want rune
	}{
		{"dsv ,", ','},
		{"dsv tab", '\t'},
		{"dsv unit", '\x1f'},
		{"dsv off", 0},
	} {
		if !r.dsvDispatch(c.cmd) || r.delim != c.want {
			t.Fatalf("%q -> delim=%q, want %q", c.cmd, r.delim, c.want)
		}
	}

	for _, c := range []struct {
		cmd   string
		field *bool
		want  bool
	}{
		{"quotes on", &r.quotes, true},
		{"quotes off", &r.quotes, false},
		{"headers on", &r.headers, true},
		{"headers off", &r.headers, false},
	} {
		if !r.dsvDispatch(c.cmd) || *c.field != c.want {
			t.Fatalf("%q -> %v, want %v", c.cmd, *c.field, c.want)
		}
	}

	// A rejected delimiter is still a matched dsv command, but leaves state alone.
	r.delim = ','
	if !r.dsvDispatch(`dsv \t`) || r.delim != ',' {
		t.Fatalf(`dsv \t should be rejected without changing delim, got %q`, r.delim)
	}

	// Bare verbs report state and match.
	for _, cmd := range []string{"dsv", "quotes", "headers"} {
		if !r.dsvDispatch(cmd) {
			t.Errorf("bare %q should match", cmd)
		}
	}

	// A non-DSV command falls through so address parsing can handle it.
	if r.dsvDispatch("5.10") {
		t.Error("5.10 should not match dsvDispatch")
	}

	// A matched command clears r.last — its report sits between block and prompt.
	r.last = &block{start: 1, count: 1}
	r.dsvDispatch("dsv ,")
	if r.last != nil {
		t.Error("a dsv command should clear r.last")
	}
}

func TestFieldSpans(t *testing.T) {
	cases := []struct {
		name   string
		line   string
		delim  rune
		quotes bool
		want   []fieldSpan
		ok     bool
	}{
		{"naive", `a,b,c`, ',', false, []fieldSpan{{0, 1, "a", false}, {2, 3, "b", false}, {4, 5, "c", false}}, true},
		{"naive empty", ``, ',', false, []fieldSpan{{0, 0, "", false}}, true},
		{"naive trailing delim", `a,`, ',', false, []fieldSpan{{0, 1, "a", false}, {2, 2, "", false}}, true},
		{"naive keeps quotes", `"a,b`, ',', false, []fieldSpan{{0, 2, `"a`, false}, {3, 4, "b", false}}, true},
		{"quoted simple", `a,b`, ',', true, []fieldSpan{{0, 1, "a", false}, {2, 3, "b", false}}, true},
		{"quoted embedded delim", `"a,b",c`, ',', true, []fieldSpan{{0, 5, "a,b", true}, {6, 7, "c", false}}, true},
		{"quoted escaped quote", `"a""b"`, ',', true, []fieldSpan{{0, 6, `a"b`, true}}, true},
		{"quoted then empty", `"x",`, ',', true, []fieldSpan{{0, 3, "x", true}, {4, 4, "", false}}, true},
		{"unbalanced quote", `"a,b`, ',', true, nil, false},
		{"stray after quote", `"a"b`, ',', true, nil, false},
	}
	for _, c := range cases {
		got, ok := fieldSpans(c.line, c.delim, c.quotes)
		if ok != c.ok {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.ok)
			continue
		}
		if ok && !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: spans = %+v, want %+v", c.name, got, c.want)
		}
	}
}

// fieldSpans' quote-aware decoding must agree with encoding/csv on well-formed
// lines — the stdlib is the oracle for the RFC quoting rules the hand-rolled
// scanner mirrors. (nved renders the raw text, not these values, but value still
// feeds round-trip-correct save, so it has to be right.)
func TestFieldSpansMatchEncodingCSV(t *testing.T) {
	lines := []string{`a,b,c`, `a,`, `"a,b",c`, `"a""b",x`, `one`, `x,"y,z","w"`}
	for _, line := range lines {
		spans, ok := fieldSpans(line, ',', true)
		if !ok {
			t.Errorf("%q: fieldSpans reported not-ok", line)
			continue
		}
		rd := csv.NewReader(strings.NewReader(line))
		rd.FieldsPerRecord = -1
		want, err := rd.Read()
		if err != nil {
			t.Errorf("%q: encoding/csv error %v", line, err)
			continue
		}
		got := make([]string, len(spans))
		for i, s := range spans {
			got[i] = s.value
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: fieldSpans values %q, encoding/csv %q", line, got, want)
		}
	}
}

func TestFieldOf(t *testing.T) {
	spans, _ := fieldSpans("a,b,c", ',', false) // [0,1] [2,3] [4,5]
	cases := []struct{ cx, want int }{{0, 0}, {1, 0}, {2, 1}, {3, 1}, {4, 2}, {5, 2}}
	for _, c := range cases {
		if got := fieldOf(spans, c.cx); got != c.want {
			t.Errorf("fieldOf cx=%d = %d, want %d", c.cx, got, c.want)
		}
	}
}

func TestAlignedVisualCol(t *testing.T) {
	spans, _ := fieldSpans("alice,30,NYC", ',', false) // [0,5] [6,8] [9,12]
	colW := []int{5, 3, 4}                             // widest cells: alice / age / city-ish
	gw := gapWidth(',')                                // comma is shown -> 3-column gap
	// Each column starts past the prior columns padded to colW plus the gap.
	cases := []struct {
		cx, want int
	}{
		{0, 0},   // start of alice
		{5, 5},   // end of alice
		{6, 8},   // start of 30: colW[0]=5 + 3 gap
		{8, 10},  // end of 30
		{9, 14},  // start of NYC: 5+3 + colW[1]=3 + 3
		{12, 17}, // end of NYC
	}
	for _, c := range cases {
		if got := alignedVisualCol(spans, colW, gw, c.cx); got != c.want {
			t.Errorf("alignedVisualCol cx=%d = %d, want %d", c.cx, got, c.want)
		}
	}
}

func TestRawCells(t *testing.T) {
	// raw keeps the quotes (what the aligned editor renders and edits); splitFields
	// would strip them to the value.
	got, ok := rawCells(`"a,b",c`, ',', true)
	if !ok || !reflect.DeepEqual(got, []string{`"a,b"`, "c"}) {
		t.Errorf("rawCells = %q ok=%v", got, ok)
	}
}

// newAlignedEditor builds a climbed-in DSV editor with output discarded, so a test
// can drive aligned navigation and geometry directly.
func newAlignedEditor(t *testing.T, lines []string, delim rune, quotes, headers bool, cy, cx int) *editor {
	t.Helper()
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	b := &buffer{lines: append([]string(nil), lines...)}
	r := &repl{b: b, termW: 80, delim: delim, quotes: quotes, headers: headers, lastAligned: true, wrap: true}
	e := &editor{r: r, start: 1, count: len(lines), cy: cy, cx: cx, aligned: true}
	e.recomputeColW()
	return e
}

func TestAlignedPhysCursor(t *testing.T) {
	e := newAlignedEditor(t, []string{"name,age,city", "alice,30,NYC"}, ',', false, true, 1, 0)
	// gutter width is 1 (two lines), so chaCol = 1 + 2 + visualCol + 1 = visualCol + 4.
	// The comma is shown, so columns are 3 apart: alice(5)+3 -> col 8, +30(2)pad to
	// 3 +3 -> col 14.
	for _, c := range []struct{ cx, wantCol int }{{0, 4}, {6, 12}, {9, 18}} {
		e.cx = c.cx
		row, col := e.physCursor()
		if row != 1 {
			t.Errorf("cx=%d row=%d, want 1 (one screen row per line)", c.cx, row)
		}
		if col != c.wantCol {
			t.Errorf("cx=%d chaCol=%d, want %d", c.cx, col, c.wantCol)
		}
	}
}

func TestAlignedCellNav(t *testing.T) {
	e := newAlignedEditor(t, []string{"alice,30,NYC"}, ',', false, false, 0, 0) // fields at 0,6,9
	e.cellRight()
	if e.cx != 6 {
		t.Errorf("cellRight from 0: cx=%d, want 6", e.cx)
	}
	e.cellRight()
	if e.cx != 9 {
		t.Errorf("cellRight from 6: cx=%d, want 9", e.cx)
	}
	e.cellLeft() // at a cell start -> previous cell start
	if e.cx != 6 {
		t.Errorf("cellLeft from 9: cx=%d, want 6", e.cx)
	}
	e.cx = 8 // partway into the middle cell
	e.cellLeft()
	if e.cx != 6 {
		t.Errorf("cellLeft from mid-cell: cx=%d, want 6", e.cx)
	}
}

func TestMoveVertAlignedKeepsColumn(t *testing.T) {
	col := func(e *editor) int { return fieldOf(e.alignedSpans(e.curLine()), e.cx) }

	// Rows whose raw field widths differ: preserving the raw rune index would drop
	// the cursor into a neighbouring cell. Down from field 2 of a short row must
	// stay in field 2 of the wide row, not skid back to field 0 at raw index 4.
	e := newAlignedEditor(t, []string{"x,y,z", "aaaaaa,bbbbbb,cccccc"}, ',', false, false, 0, 4)
	e.moveVertAligned(1)
	if c := col(e); c != 2 {
		t.Errorf("down into wide row: landed in field %d (cx=%d), want 2", c, e.cx)
	}

	// Moving up INTO a row whose middle field is empty must land in that empty cell.
	e2 := newAlignedEditor(t, []string{"a,,ccc", "bb,dd,ee"}, ',', false, false, 1, 3) // field 1 ("dd")
	e2.moveVertAligned(0)
	if c := col(e2); c != 1 || e2.cx != 2 {
		t.Errorf("up into empty cell: field %d cx=%d, want field 1 cx=2", c, e2.cx)
	}

	// Moving OUT of an empty cell: raw index 2 would land in field 0 of the wider
	// row below; the fix keeps the column at field 1.
	e3 := newAlignedEditor(t, []string{"a,,ccc", "bb,dd,ee"}, ',', false, false, 0, 2) // empty field 1
	e3.moveVertAligned(1)
	if c := col(e3); c != 1 {
		t.Errorf("down out of empty cell: landed in field %d (cx=%d), want 1", c, e3.cx)
	}

	// The intra-cell offset clamps to a shorter target cell: offset 4 inside a wide
	// field 0 lands at the end of the one-rune field 0 above, still in field 0.
	e4 := newAlignedEditor(t, []string{"x,y,z", "aaaaaa,bbbbbb,cccccc"}, ',', false, false, 1, 4)
	e4.moveVertAligned(0)
	if c := col(e4); c != 0 || e4.cx != 1 {
		t.Errorf("up with offset clamp: field %d cx=%d, want field 0 cx=1", c, e4.cx)
	}
}

func TestAlignedHorizontalPan(t *testing.T) {
	e := newAlignedEditor(t, []string{"aaaa,bbbb,cccc,dddd,eeee"}, ',', false, false, 0, 0)
	e.r.termW = 16 // gutter 1 + 2 spaces -> 13 columns of text
	// At the left edge nothing is hidden, so there is no pan.
	if e.panToCursor() || e.hscroll != 0 {
		t.Fatalf("start: pan=true or hscroll=%d, want no pan / 0", e.hscroll)
	}
	// Jump to the end of the wide row: it must pan, and the cursor must land back
	// on screen (past the gutter, within the terminal width).
	e.cx = e.lineLen(0)
	if !e.panToCursor() {
		t.Fatal("cursor at the end of a wide row should pan")
	}
	if e.hscroll == 0 {
		t.Fatal("hscroll should advance past 0")
	}
	if _, col := e.physCursor(); col < 4 || col > e.r.termW {
		t.Errorf("panned cursor col=%d, want within (gutter, %d]", col, e.r.termW)
	}
	// Back to the start pans home again.
	e.cx = 0
	if !e.panToCursor() || e.hscroll != 0 {
		t.Errorf("return to start: hscroll=%d, want 0", e.hscroll)
	}
}

// newWrapOffEditor builds a plain-text editor with wrap off — the windowing,
// hscroll-panning text view that shares its machinery with the aligned view.
func newWrapOffEditor(t *testing.T, lines []string, cy, cx int) *editor {
	t.Helper()
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	b := &buffer{lines: append([]string(nil), lines...)}
	r := &repl{b: b, termW: 80, wrap: false}
	return &editor{r: r, start: 1, count: len(lines), cy: cy, cx: cx}
}

func TestWrapOffPhysCursor(t *testing.T) {
	e := newWrapOffEditor(t, []string{"hello world foo"}, 0, 0)
	e.r.termW = 13 // gutter 1 + 2 -> A = 10, but wrap off never wraps
	// One screen row per line: the row offset is always 0, never a wrapped row.
	for _, cx := range []int{0, 6, 10} {
		e.cx = cx
		if row, _ := e.physCursor(); row != 0 {
			t.Errorf("cx=%d row=%d, want 0 (wrap off is one row per line)", cx, row)
		}
	}
	// The column is visualCol past the gutter (width 1 + 2), plus 1, less the pan.
	e.cx, e.hscroll = 2, 0
	if _, col := e.physCursor(); col != 6 { // 3 + 2 - 0 + 1
		t.Errorf("unpanned cx=2 col=%d, want 6", col)
	}
	e.cx, e.hscroll = 8, 5
	if _, col := e.physCursor(); col != 7 { // 3 + 8 - 5 + 1
		t.Errorf("panned cx=8 hscroll=5 col=%d, want 7", col)
	}
}

func TestWrapOffPan(t *testing.T) {
	e := newWrapOffEditor(t, []string{"a very wide plain text line that exceeds the window"}, 0, 0)
	e.r.termW = 16 // gutter 1 + 2 -> 13 columns of text
	// At the left edge nothing is hidden, so there is no pan.
	if e.panToCursor() || e.hscroll != 0 {
		t.Fatalf("start: pan or hscroll=%d, want no pan / 0", e.hscroll)
	}
	// Jump to the end of the wide line: it must pan, and the cursor must land back
	// on screen (past the gutter, within the terminal width).
	e.cx = e.lineLen(0)
	if !e.panToCursor() {
		t.Fatal("cursor at the end of a wide line should pan")
	}
	if e.hscroll == 0 {
		t.Fatal("hscroll should advance past 0")
	}
	if _, col := e.physCursor(); col < 4 || col > e.r.termW {
		t.Errorf("panned cursor col=%d, want within (gutter, %d]", col, e.r.termW)
	}
	// Back to the start pans home again.
	e.cx = 0
	if !e.panToCursor() || e.hscroll != 0 {
		t.Errorf("return to start: hscroll=%d, want 0", e.hscroll)
	}
}

func TestAlignedHeadRows(t *testing.T) {
	// header at the top of the block (start 1) is in-block: one head row (status).
	e := newAlignedEditor(t, []string{"name,age", "a,1"}, ',', false, true, 0, 0)
	if e.sticky() || e.headRows() != 1 {
		t.Errorf("start=1: sticky=%v headRows=%d, want false/1", e.sticky(), e.headRows())
	}
	// scrolled past line 1 with headers on: the header pins, adding a row.
	e.start = 5
	if !e.sticky() || e.headRows() != 2 {
		t.Errorf("start=5: sticky=%v headRows=%d, want true/2", e.sticky(), e.headRows())
	}
}

func TestAlignedInsert(t *testing.T) {
	e := newAlignedEditor(t, []string{"a,bb,c"}, ',', false, false, 0, 1) // after 'a'
	e.insert('X')
	if e.r.b.lines[0] != "aX,bb,c" || e.cx != 2 {
		t.Errorf("after insert: line=%q cx=%d, want %q/2", e.r.b.lines[0], e.cx, "aX,bb,c")
	}
	if !e.r.b.modified {
		t.Error("insert should mark the buffer modified")
	}
}

func TestInQuotedValue(t *testing.T) {
	// `x,"a,b",c`: field 1 is the quoted "a,b" — opening quote at rune 2, closing at
	// rune 6, so its raw span is 2..7. A delimiter is data only at a cursor strictly
	// between the quotes: position 3 sits just inside the opening quote, position 7
	// just past the closing one.
	e := newAlignedEditor(t, []string{`x,"a,b",c`}, ',', true, false, 0, 0)
	for _, c := range []struct {
		cx   int
		want bool
	}{
		{0, false}, // unquoted field 0
		{2, false}, // before the opening quote — comma here splits the row
		{3, true},  // just inside the opening quote
		{6, true},  // just before the closing quote — still inside
		{7, false}, // just past the closing quote — comma here splits the row
		{8, false}, // unquoted final field
	} {
		e.cx = c.cx
		if got := e.inQuotedValue(); got != c.want {
			t.Errorf("inQuotedValue cx=%d = %v, want %v", c.cx, got, c.want)
		}
	}
	// With quotes off, a quoted-looking field is just text — never a value home for
	// the delimiter.
	e2 := newAlignedEditor(t, []string{`"a,b"`}, ',', false, false, 0, 2)
	if e2.inQuotedValue() {
		t.Error("quotes off: inQuotedValue should be false")
	}
}

func TestAlignedReflowWidens(t *testing.T) {
	// col0 spans "a" and "bb" -> width 2. Growing line 1's first cell past that must
	// widen the column (the reflow trigger that picks a full repaint over a redraw).
	e := newAlignedEditor(t, []string{"a,x", "bb,y"}, ',', false, false, 0, 1)
	before := e.colW[0]
	e.insert('a')
	e.insert('a') // "aaa,x"
	if e.colW[0] <= before {
		t.Errorf("col0 width should grow past %d, got %d", before, e.colW[0])
	}
}

func TestAlignedBackspaceChar(t *testing.T) {
	e := newAlignedEditor(t, []string{"ab,c"}, ',', false, false, 0, 2) // after "ab"
	e.backspace()
	if e.r.b.lines[0] != "a,c" || e.cx != 1 {
		t.Errorf("backspace mid-cell: line=%q cx=%d, want %q/1", e.r.b.lines[0], e.cx, "a,c")
	}
}

func TestAlignedStructuralEditsGuarded(t *testing.T) {
	// Backspace at a row's start would join rows; Delete at a row's end would pull
	// the next row up. Both are structural — suppressed in aligned mode.
	e := newAlignedEditor(t, []string{"a,b", "c,d"}, ',', false, false, 1, 0) // start of row 2
	e.backspace()
	if len(e.r.b.lines) != 2 || e.r.b.lines[0] != "a,b" {
		t.Errorf("backspace at row start should be a no-op, got %q", e.r.b.lines)
	}
	e.cy, e.cx = 0, 3 // end of row 1 ("a,b")
	e.del()
	if len(e.r.b.lines) != 2 {
		t.Errorf("del at row end should be a no-op, got %q", e.r.b.lines)
	}
}

func TestAlignedSaveVerbatim(t *testing.T) {
	// Always-raw means save is a verbatim join: a quoted field round-trips with its
	// quotes intact, no re-serialization pass.
	p := filepath.Join(t.TempDir(), "f.csv")
	e := newAlignedEditor(t, []string{"name,note", `bob,"a,b"`}, ',', true, true, 1, 0)
	e.r.b.name = p
	e.insert('B') // bob -> Bbob, leaving the quoted note untouched
	if _, err := e.r.b.save(); err != nil {
		t.Fatal(err)
	}
	b2, _, err := openBuffer(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"name,note", `Bbob,"a,b"`}
	if !reflect.DeepEqual(b2.lines, want) {
		t.Errorf("round-trip = %q, want %q", b2.lines, want)
	}
}

func TestAlignedUnbalancedQuoteNoCrash(t *testing.T) {
	// Typing a lone quote leaves the line transiently unparseable; the editor must
	// render and navigate it as one raw cell rather than panic.
	e := newAlignedEditor(t, []string{`a,"b"`}, ',', true, false, 0, 0)
	e.insert('"') // `"a,"b"` — odd number of quotes
	cells := e.alignedCells(e.curLine())
	if len(cells) != 1 || cells[0] != `"a,"b"` {
		t.Errorf("unbalanced line should be one raw cell, got %q", cells)
	}
	e.cx = len([]rune(e.curLine()))
	if _, col := e.physCursor(); col < 1 {
		t.Errorf("physCursor on unbalanced line returned col %d", col)
	}
}

func TestColWidths(t *testing.T) {
	rows := [][]string{
		{"name", "age"},
		{"alice", "30"},
		{"bob", "7"},
	}
	if got := colWidths(rows); !reflect.DeepEqual(got, []int{5, 3}) {
		t.Errorf("colWidths = %v, want [5 3]", got)
	}
	// a ragged row extends the grid; the new column is sized by what reaches it.
	ragged := [][]string{{"a"}, {"aa", "bbb"}}
	if got := colWidths(ragged); !reflect.DeepEqual(got, []int{2, 3}) {
		t.Errorf("colWidths ragged = %v, want [2 3]", got)
	}
	// a multi-byte cell counts runes, not bytes.
	if got := colWidths([][]string{{"ü"}}); !reflect.DeepEqual(got, []int{1}) {
		t.Errorf("colWidths utf8 = %v, want [1]", got)
	}
}

func TestAlignRow(t *testing.T) {
	w := []int{5, 3}
	// the comma is shown in the gap: "bob" padded to 5, then " , ", then the last
	// cell unpadded.
	text, sep := alignRow([]string{"bob", "7"}, w, ',')
	if text != "bob   , 7" {
		t.Errorf("alignRow = %q, want %q", text, "bob   , 7")
	}
	if rs := []rune(text); !sep[6] || rs[6] != ',' {
		t.Errorf("sep should mark the comma at index 6, got rune %q sep %v", rs[6], sep[6])
	}
	for i, s := range sep {
		if s && i != 6 {
			t.Errorf("only the comma should be masked, but sep[%d] is set too", i)
		}
	}
	// a short row gets a blank padding gap for the column it lacks — no phantom
	// delimiter past its data: "a" padded to 5, then three blank columns.
	short, sshort := alignRow([]string{"a"}, w, ',')
	if short != "a"+strings.Repeat(" ", 7) {
		t.Errorf("alignRow short = %q, want %q", short, "a"+strings.Repeat(" ", 7))
	}
	for i, s := range sshort {
		if s {
			t.Errorf("short row should mask nothing, but sep[%d] is set", i)
		}
	}
	// a non-printable delimiter keeps the original two-space gap, no inline char.
	tabbed, stab := alignRow([]string{"bob", "7"}, w, '\t')
	if tabbed != "bob    7" {
		t.Errorf("alignRow tab = %q, want %q", tabbed, "bob    7")
	}
	for _, s := range stab {
		if s {
			t.Error("tab delimiter should mask nothing")
		}
	}
	// a comma inside a quoted cell is data: only the separator comma is masked, even
	// though the cell text holds one too.
	q, sq := alignRow([]string{`"a,b"`, "c"}, []int{5, 1}, ',')
	rs := []rune(q)
	masked := 0
	for i, s := range sq {
		if s {
			masked++
			if rs[i] != ',' {
				t.Errorf("masked rune %q is not a comma", rs[i])
			}
		}
	}
	if masked != 1 {
		t.Errorf("only the separator comma should be masked, got %d in %q", masked, q)
	}
}

func TestEmitWindowedRowShowsFaintDelim(t *testing.T) {
	var buf strings.Builder
	old := screen
	screen = &buf
	t.Cleanup(func() { screen = old })
	text, sep := alignRow([]string{"a", "b"}, []int{1, 1}, ',')
	emitWindowedRow(1, 1, 0, 40, text, sep, false, 0, 0)
	if got := buf.String(); !strings.Contains(got, faint(",")) {
		t.Errorf("rendered row should contain a faint comma, got %q", got)
	}
}

// A raw wrap-off row passes a nil separator mask; emitWindowedRow must render it
// without panicking and without inventing any faint runes.
func TestEmitWindowedRowRawNilMask(t *testing.T) {
	var buf strings.Builder
	old := screen
	screen = &buf
	t.Cleanup(func() { screen = old })
	emitWindowedRow(1, 1, 0, 40, "hello world", nil, false, 0, 0)
	if got := buf.String(); !strings.Contains(got, "hello world") {
		t.Errorf("rendered row should contain the plain text, got %q", got)
	}
}

func TestWindow(t *testing.T) {
	// fits with no pan: whole row, no markers.
	if l, lo, hi, r := window(5, 0, 10); l || r || lo != 0 || hi != 5 {
		t.Errorf("window fit = (%v,%d,%d,%v), want (false,0,5,false)", l, lo, hi, r)
	}
	// overflow at the right: marker takes the last column, body is [0,2).
	if l, lo, hi, r := window(5, 0, 3); l || !r || lo != 0 || hi != 2 {
		t.Errorf("window right cut = (%v,%d,%d,%v), want (false,0,2,true)", l, lo, hi, r)
	}
	// panned into the middle of a 10-wide row: both markers, body is [4,7).
	if l, lo, hi, r := window(10, 3, 5); !l || !r || lo != 4 || hi != 7 {
		t.Errorf("window mid = (%v,%d,%d,%v), want (true,4,7,true)", l, lo, hi, r)
	}
	// panned to the tail of an 8-wide row: left marker only, body [6,8).
	if l, lo, hi, r := window(8, 5, 5); !l || r || lo != 6 || hi != 8 {
		t.Errorf("window tail = (%v,%d,%d,%v), want (true,6,8,false)", l, lo, hi, r)
	}
}

func TestAlignedPhysHeightIsOne(t *testing.T) {
	r := newRepl([]string{"a,b,c", "this is a very long field value that would wrap when raw"}, 20, 24)
	if got := r.physHeight(2); got <= 1 {
		t.Fatalf("raw physHeight(2) = %d, want > 1 (the long line wraps)", got)
	}
	r.delim = ','
	if got := r.physHeight(2); got != 1 {
		t.Errorf("aligned physHeight(2) = %d, want 1", got)
	}
}

func TestWrapDispatch(t *testing.T) {
	r := newRepl([]string{"hello"}, 80, 24) // wrap defaults on
	// on / off set the flag, and each is a matched command.
	if !r.wrapDispatch("wrap off") || r.wrap {
		t.Fatalf("wrap off -> wrap=%v, want false", r.wrap)
	}
	if !r.wrapDispatch("wrap on") || !r.wrap {
		t.Fatalf("wrap on -> wrap=%v, want true", r.wrap)
	}
	// A bare verb reports state and still matches.
	if !r.wrapDispatch("wrap") {
		t.Error("bare wrap should match")
	}
	// A bad argument is matched but leaves the flag alone.
	r.wrap = true
	if !r.wrapDispatch("wrap sideways") || !r.wrap {
		t.Errorf("a bad wrap arg should match without changing the flag, got %v", r.wrap)
	}
	// A non-wrap command falls through so address parsing can handle it.
	if r.wrapDispatch("5.10") {
		t.Error("5.10 should not match wrapDispatch")
	}
	// A matched command clears r.last — its report sits between block and prompt.
	r.last = &block{start: 1, count: 1}
	r.wrapDispatch("wrap off")
	if r.last != nil {
		t.Error("a matched wrap command should clear r.last")
	}
}

func TestWrapOffPhysHeightIsOne(t *testing.T) {
	r := newRepl([]string{"a", "this is a very long line that would wrap when wrap is on"}, 20, 24)
	if got := r.physHeight(2); got <= 1 {
		t.Fatalf("wrap on physHeight(2) = %d, want > 1 (the long line wraps)", got)
	}
	r.wrap = false
	if got := r.physHeight(2); got != 1 {
		t.Errorf("wrap off physHeight(2) = %d, want 1", got)
	}
}

func TestPrintLinesWrapOffSmoke(t *testing.T) {
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	r := newRepl([]string{"short", "a line wide enough to be windowed off the right edge of a narrow view"}, 30, 24)
	r.wrap = false
	r.printLines(1, 2) // exercises the nil-mask windowed raw print path
	if r.last == nil {
		t.Fatal("wrap-off print should record a climbable block")
	}
	if r.lastAligned {
		t.Error("a plain-text wrap-off block is not aligned")
	}
}

func TestPrintLinesAlignedSmoke(t *testing.T) {
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	r := newRepl([]string{"name,age", "alice,30", "bob,7"}, 80, 24)
	r.delim, r.quotes, r.headers = ',', true, true
	r.printLines(2, 3) // header has scrolled off -> sticky path
	if r.last == nil || r.last.start != 2 || r.last.count != 2 {
		t.Fatalf("printLines aligned r.last = %+v, want start=2 count=2", r.last)
	}
}

func TestSplitRecords(t *testing.T) {
	if got := splitRecords("a\x1eb\x1ec", '\x1e'); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("splitRecords RS = %q", got)
	}
	if got := splitRecords("a\x1eb\x1e", '\x1e'); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("splitRecords trailing-RS dropped = %q", got)
	}
	if got := splitRecords("", '\x1e'); !reflect.DeepEqual(got, []string{""}) {
		t.Errorf("splitRecords empty = %q, want one empty record", got)
	}
}

func TestRelineRoundTrip(t *testing.T) {
	// A newline-lined buffer re-lined to RS and back is unchanged, and the undo
	// stack is cleared by the reload.
	b := &buffer{lines: []string{"a", "b", "c"}, undos: []undoEntry{{}}}
	b.reline('\x1e')
	if b.recSep != '\x1e' || len(b.undos) != 0 {
		t.Fatalf("reline to RS: recSep=%q undos=%d", b.recSep, len(b.undos))
	}
	// On RS, the original newlines are gone — the three records hold no newline,
	// so a buffer that was one long RS line re-lines into the records.
	one := &buffer{lines: []string{"a\x1eb\x1ec"}} // a pure-RS file loads as one line
	one.reline('\x1e')
	if !reflect.DeepEqual(one.lines, []string{"a", "b", "c"}) {
		t.Fatalf("reline pure-RS file = %q, want [a b c]", one.lines)
	}
	one.reline('\n') // back to newline: no newlines present, collapses to one line
	if !reflect.DeepEqual(one.lines, []string{"a\x1eb\x1ec"}) {
		t.Fatalf("reline back to newline = %q, want the one RS-joined line", one.lines)
	}
}

func TestSaveRecordSep(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.bin")
	b := &buffer{name: p, lines: []string{"a", "b", "c"}, recSep: '\x1e'}
	if _, err := b.save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "a\x1eb\x1ec\x1e" {
		t.Errorf("save with RS = %q, want RS-joined with trailing RS", data)
	}
}

func TestLinebreaksCommand(t *testing.T) {
	r := newRepl([]string{"a\x1eb\x1ec"}, 80, 24)
	r.last = &block{start: 1, count: 1}
	if !r.dsvDispatch("linebreaks record") || r.b.sep() != '\x1e' {
		t.Fatalf("linebreaks record -> sep=%q", r.b.sep())
	}
	if !reflect.DeepEqual(r.b.lines, []string{"a", "b", "c"}) {
		t.Fatalf("linebreaks record should re-line: %q", r.b.lines)
	}
	if r.last != nil {
		t.Error("linebreaks should clear r.last (a reload resets the block)")
	}
	if !r.dsvDispatch("linebreaks newline") || r.b.sep() != '\n' {
		t.Fatalf("linebreaks newline -> sep=%q", r.b.sep())
	}
	// A bad argument is matched (it is a linebreaks command) but changes nothing.
	if !r.dsvDispatch("linebreaks tab") || r.b.sep() != '\n' {
		t.Errorf("linebreaks tab should be rejected, sep=%q", r.b.sep())
	}
	// The old `rows` verb is no longer a dsv command — it belongs to structDispatch.
	if r.dsvDispatch("rows record") {
		t.Error("rows should no longer be a dsv command (renamed to linebreaks)")
	}
}

func TestRowsClauseInState(t *testing.T) {
	// The record layer is invisible in the field-layer state line until it is
	// non-default. Once the buffer is record-lined, every state report names it,
	// so "plain text" can't hide a buffer that would save 0x1E-terminated — the
	// trap that an asv preset followed by dsv off used to spring silently.
	r := newRepl([]string{"a\x1eb"}, 80, 24)
	if got := r.dsvState(); got != "delimiter: off — plain text" {
		t.Errorf("newline-lined off state = %q, want no linebreaks clause", got)
	}
	r.b.reline('\x1e')
	if got := r.dsvState(); got != "delimiter: off — plain text, linebreaks record" {
		t.Errorf("record-lined off state = %q, want trailing linebreaks clause", got)
	}
	r.delim, r.quotes, r.headers = '\x1f', false, true
	want := "delimiter: unit separator (0x1F), quotes off, headers on, linebreaks record"
	if got := r.dsvState(); got != want {
		t.Errorf("record-lined asv state = %q, want %q", got, want)
	}
}

func TestPresets(t *testing.T) {
	// Presets go through dispatch — the same path +spec runs on startup, so
	// nved +csv f and nved +asv f open straight into the aligned view.
	for _, c := range []struct {
		cmd             string
		delim           rune
		quotes, headers bool
		recSep          rune
	}{
		{"csv", ',', true, true, '\n'},
		{"tsv", '\t', true, true, '\n'},
		{"asv", '\x1f', false, true, '\x1e'},
	} {
		r := newRepl([]string{"a,b,c"}, 80, 24)
		if r.dispatch(c.cmd) {
			t.Fatalf("%s should not quit", c.cmd)
		}
		if r.delim != c.delim || r.quotes != c.quotes || r.headers != c.headers || r.b.sep() != c.recSep {
			t.Errorf("%s -> delim=%q quotes=%v headers=%v sep=%q",
				c.cmd, r.delim, r.quotes, r.headers, r.b.sep())
		}
	}
}

func TestExitAliases(t *testing.T) {
	// Every alias quits a clean buffer at once.
	for _, cmd := range []string{"x", "exit", "q", "quit"} {
		b := &buffer{lines: []string{"hi"}}
		if !(&repl{b: b}).dispatch(cmd) {
			t.Errorf("%q on a clean buffer should quit", cmd)
		}
	}
	// Warn-twice spans the aliases: a first exit (any alias) arms, a second
	// (any alias) discards.
	b := &buffer{lines: []string{"hi"}, modified: true}
	r := &repl{b: b}
	if r.dispatch("q") {
		t.Fatal("first exit on a dirty buffer should warn, not quit")
	}
	if !b.exitArmed {
		t.Fatal("first exit should arm the warning")
	}
	if !r.dispatch("exit") {
		t.Fatal("second exit on a dirty buffer should discard and quit")
	}
}

func TestSearchForward(t *testing.T) {
	lines := []string{"alpha beta", "gamma", "beta gamma"}
	re := regexp.MustCompile("beta")

	// First match anywhere, scanning from the top.
	line, lo, hi, wrapped, ok := searchForward(lines, re, 0, 0)
	if !ok || line != 0 || lo != 6 || hi != 10 || wrapped {
		t.Fatalf("first = (%d,%d,%d,wrapped=%v,ok=%v), want (0,6,10,false,true)", line, lo, hi, wrapped, ok)
	}

	// Next from just past it: nothing more on line 0, so it lands on line 2.
	line, lo, hi, wrapped, ok = searchForward(lines, re, 0, 10)
	if !ok || line != 2 || lo != 0 || hi != 4 || wrapped {
		t.Fatalf("second = (%d,%d,%d,wrapped=%v), want (2,0,4,false)", line, lo, hi, wrapped)
	}

	// Next from past the last match wraps to the top and flags it.
	line, lo, hi, wrapped, ok = searchForward(lines, re, 2, 4)
	if !ok || line != 0 || lo != 6 || !wrapped {
		t.Fatalf("wrap = (%d,%d,%d,wrapped=%v), want (0,6,_,true)", line, lo, hi, wrapped)
	}

	// No match at all.
	if _, _, _, _, ok := searchForward(lines, regexp.MustCompile("zzz"), 0, 0); ok {
		t.Error("zzz should not match")
	}
}

func TestSearchForwardUnicode(t *testing.T) {
	// Multibyte runes before the match: the returned range must be rune indices,
	// not byte offsets, so the cursor and highlight land on the right characters.
	lines := []string{"Þórðarson xy"}
	line, lo, hi, _, ok := searchForward(lines, regexp.MustCompile("xy"), 0, 0)
	if !ok || line != 0 || lo != 10 || hi != 12 {
		t.Fatalf("unicode = (%d,%d,%d), want (0,10,12)", line, lo, hi)
	}
}

func TestFindDispatch(t *testing.T) {
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	r := newRepl([]string{"foo", "bar", "foo baz"}, 80, 24)

	// A pattern starts a search, records it, and prints a climbable block.
	if !r.findDispatch("find foo") || r.search == nil || r.search.pat != "foo" || r.search.line != 0 || r.last == nil {
		t.Fatalf("find foo -> search=%+v last=%v", r.search, r.last)
	}
	// ...and arms the next prompt so Enter steps without retyping.
	if r.pendingLine != "find next" {
		t.Errorf("find should arm pendingLine, got %q", r.pendingLine)
	}
	// next steps to the following match...
	r.findDispatch("find next")
	if r.search.line != 2 {
		t.Fatalf("find next -> line %d, want 2", r.search.line)
	}
	// ...and wraps back to the top.
	r.findDispatch("f next")
	if r.search.line != 0 {
		t.Fatalf("f next (wrap) -> line %d, want 0", r.search.line)
	}

	// A bare verb reports state and clears the block but keeps the search.
	r.findDispatch("find")
	if r.search == nil || r.last != nil {
		t.Errorf("bare find -> search=%v last=%v, want search kept, last cleared", r.search, r.last)
	}

	// No match disarms the search and clears the block.
	r.findDispatch("find nomatchxyz")
	if r.search != nil || r.last != nil {
		t.Errorf("no match -> search=%v last=%v, want both nil", r.search, r.last)
	}

	// A bad pattern is a matched-but-rejected command.
	if !r.findDispatch("find [") {
		t.Error("a bad pattern should still be a matched find command")
	}

	// next with nothing armed is handled, not a fall-through.
	r.search = nil
	if !r.findDispatch("find next") {
		t.Error("find next with no search should still match")
	}

	// Non-find input falls through to address parsing.
	for _, s := range []string{"format", "5.10", "fo", "5"} {
		if r.findDispatch(s) {
			t.Errorf("%q should not match findDispatch", s)
		}
	}
}

func TestFindPrintsMatchBlock(t *testing.T) {
	var buf strings.Builder
	old := screen
	screen = &buf
	t.Cleanup(func() { screen = old })
	r := newRepl([]string{"one", "two", "needle here", "four"}, 80, 24)
	r.findDispatch("find needle")
	// Strip SGR so the highlight (which splits the match into per-rune reverse
	// spans) doesn't hide the logical line content from the contains check.
	out := regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`).ReplaceAllString(buf.String(), "")
	if !strings.Contains(out, "needle here") {
		t.Errorf("find should print the matching line, got %q", out)
	}
	// The match block leads with the match line, so it is climbable from the top.
	if r.last == nil || r.last.start != 3 {
		t.Errorf("match block should start at line 3, got %v", r.last)
	}
}

func TestFindHighlightsMatch(t *testing.T) {
	var buf strings.Builder
	old := screen
	screen = &buf
	t.Cleanup(func() { screen = old })

	// Raw text: the matched substring is drawn reversed, the rest is not.
	r := newRepl([]string{"the needle is here"}, 80, 24)
	r.findDispatch("find needle")
	if got := buf.String(); !strings.Contains(got, reverse("n")) {
		t.Errorf("raw match should be reverse-video, got %q", got)
	}

	// Multibyte runes before the match: the highlight lands on the match, not
	// shifted by the extra bytes — visualCol maps runes, not bytes.
	buf.Reset()
	r = newRepl([]string{"Þórð xy"}, 80, 24)
	r.findDispatch("find xy")
	got := buf.String()
	if !strings.Contains(got, reverse("x")) || strings.Contains(got, reverse("Þ")) {
		t.Errorf("unicode highlight misaligned, got %q", got)
	}

	// Aligned DSV: a cell value matched inside the grid is highlighted on the
	// aligned row, mapped through alignedVisualCol.
	buf.Reset()
	r = newRepl([]string{"id,city", "1,Brussels"}, 80, 24)
	r.dsvDispatch("csv")
	r.findDispatch("find Brussels")
	if got := buf.String(); !strings.Contains(got, reverse("B")) {
		t.Errorf("aligned match should be highlighted, got %q", got)
	}
}

func TestParseReplaceSpec(t *testing.T) {
	cases := []struct {
		spec      string
		old, repl string
		wantErr   bool
	}{
		{"/old/new/", "old", "new", false},
		{"/old/new", "old", "new", false},   // trailing delimiter optional
		{" /old/new/", "old", "new", false}, // leading space (verb-space form)
		{",a,b,", "a", "b", false},          // any non-alnum delimiter
		{"#a#b#", "a", "b", false},
		{"/foo/", "foo", "", false}, // replace with empty = delete
		{"/a/b/c/", "", "", true},   // text past closing delimiter
		{"xoldxnewx", "", "", true}, // alphanumeric delimiter rejected
		{"3a3b3", "", "", true},     // digit delimiter rejected
		{"", "", "", true},          // empty
		{"/only", "", "", true},     // no closing delimiter
		{"//new/", "", "", true},    // empty old pattern
	}
	for _, c := range cases {
		old, repl, err := parseReplaceSpec(c.spec)
		if (err != nil) != c.wantErr {
			t.Errorf("parseReplaceSpec(%q) err=%v, wantErr=%v", c.spec, err, c.wantErr)
			continue
		}
		if !c.wantErr && (old != c.old || repl != c.repl) {
			t.Errorf("parseReplaceSpec(%q) = (%q,%q), want (%q,%q)", c.spec, old, repl, c.old, c.repl)
		}
	}
}

func TestReplaceVerbMatching(t *testing.T) {
	// matchVerb: bare, space form, no-space delimiter form; alnum after verb rejects.
	for _, c := range []struct {
		s, verb, rest string
		ok            bool
	}{
		{"r", "r", "", true},
		{"r /a/b/", "r", " /a/b/", true},
		{"r/a/b/", "r", "/a/b/", true},
		{"rows", "r", "", false}, // alnum after r
		{"rn", "r", "", false},   // short form not a bare-r match
		{"ra/a/b/", "ra", "/a/b/", true},
		{"replace", "replace", "", true},
	} {
		rest, ok := matchVerb(c.s, c.verb)
		if ok != c.ok || (ok && rest != c.rest) {
			t.Errorf("matchVerb(%q,%q) = (%q,%v), want (%q,%v)", c.s, c.verb, rest, ok, c.rest, c.ok)
		}
	}
	// cutAllKeyword spots the global keyword only as a leading bareword.
	for _, c := range []struct {
		rest, spec string
		ok         bool
	}{
		{" all /a/b/", " /a/b/", true},
		{"all/a/b/", "/a/b/", true},
		{"/all/x/", "", false}, // old pattern is "all", not the keyword
	} {
		spec, ok := cutAllKeyword(c.rest)
		if ok != c.ok || (ok && spec != c.spec) {
			t.Errorf("cutAllKeyword(%q) = (%q,%v), want (%q,%v)", c.rest, spec, ok, c.spec, c.ok)
		}
	}
}

func TestReplaceStepping(t *testing.T) {
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	r := newRepl([]string{"foo here", "and foo and foo", "bar"}, 80, 24)

	// Preview-first: the spec arms but changes nothing yet.
	r.replaceDispatch("replace /foo/X/")
	if r.b.lines[0] != "foo here" {
		t.Fatalf("preview should not change the buffer, got %q", r.b.lines[0])
	}
	if r.search == nil || !r.search.isRepl || r.search.line != 0 {
		t.Fatalf("replace should arm on the first match, got %+v", r.search)
	}
	// First candidate is unreplaced, so the first prompt reads bare "replace".
	if r.pendingLine != "replace" {
		t.Errorf("first arm should be bare replace, got %q", r.pendingLine)
	}
	// Bare replace, while armed, applies the highlighted candidate and advances,
	// then re-arms to "replace next" for the subsequent steps.
	r.replaceDispatch("replace")
	if r.b.lines[0] != "X here" {
		t.Fatalf("first step -> %q, want %q", r.b.lines[0], "X here")
	}
	if r.search == nil || r.search.line != 1 {
		t.Fatalf("step should advance to line 1, got %+v", r.search)
	}
	if r.pendingLine != "replace next" {
		t.Errorf("after the first step, arm should be replace next, got %q", r.pendingLine)
	}
	r.replaceDispatch("rn") // short form
	r.replaceDispatch("rn")
	if r.b.lines[1] != "and X and X" {
		t.Fatalf("after stepping line 1 -> %q, want %q", r.b.lines[1], "and X and X")
	}
	// All three replaced; the next step finds nothing and disarms.
	if r.search != nil {
		t.Errorf("after exhausting matches, search should disarm, got %+v", r.search)
	}

	// Undo peels the replacements back one at a time (LIFO).
	r.undoAtPrompt()
	if r.b.lines[1] != "and X and foo" {
		t.Errorf("one undo -> %q, want %q", r.b.lines[1], "and X and foo")
	}
}

func TestReplaceAll(t *testing.T) {
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	r := newRepl([]string{"foo a foo", "b", "foo c"}, 80, 24)
	r.replaceDispatch("replace all /foo/Z/")
	if r.b.lines[0] != "Z a Z" || r.b.lines[2] != "Z c" {
		t.Fatalf("replace all -> %q / %q", r.b.lines[0], r.b.lines[2])
	}
	// One undo entry reverts the whole run.
	r.undoAtPrompt()
	if r.b.lines[0] != "foo a foo" || r.b.lines[2] != "foo c" {
		t.Errorf("undo of replace all -> %q / %q", r.b.lines[0], r.b.lines[2])
	}

	// ra short form, with a capture-group reference in the replacement.
	r.replaceDispatch(`ra /(foo) (a)/$2-$1/`)
	if r.b.lines[0] != "a-foo foo" {
		t.Errorf("ra with backref -> %q, want %q", r.b.lines[0], "a-foo foo")
	}
}

func TestReplaceDispatchFallThrough(t *testing.T) {
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	r := newRepl([]string{"hello"}, 80, 24)
	for _, s := range []string{"rationale", "5.10", "rows", "5"} {
		if r.replaceDispatch(s) {
			t.Errorf("%q should not match replaceDispatch", s)
		}
	}
}

// --- columns ---------------------------------------------------------------

func TestInsertColumnCells(t *testing.T) {
	base := []string{"a", "b", "c"}
	cases := []struct {
		p    int
		want []string
	}{
		{0, []string{"", "a", "b", "c"}}, // prepend
		{1, []string{"a", "", "b", "c"}}, // right of column 1
		{3, []string{"a", "b", "c", ""}}, // at the end
		{9, []string{"a", "b", "c", ""}}, // past the end clamps to append
		{-1, []string{"", "a", "b", "c"}},
	}
	for _, c := range cases {
		if got := insertColumnCells(base, c.p); !reflect.DeepEqual(got, c.want) {
			t.Errorf("insertColumnCells(%v, %d) = %v, want %v", base, c.p, got, c.want)
		}
	}
	if !reflect.DeepEqual(base, []string{"a", "b", "c"}) {
		t.Errorf("insertColumnCells mutated its input: %v", base)
	}
}

func TestDeleteColumnCells(t *testing.T) {
	base := []string{"a", "b", "c"}
	if got, ok := deleteColumnCells(base, 1); !ok || !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Errorf("deleteColumnCells(%v, 1) = %v, %v", base, got, ok)
	}
	if got, ok := deleteColumnCells(base, 5); ok || !reflect.DeepEqual(got, base) {
		t.Errorf("out-of-range delete should be a no-op, got %v, %v", got, ok)
	}
	if !reflect.DeepEqual(base, []string{"a", "b", "c"}) {
		t.Errorf("deleteColumnCells mutated its input: %v", base)
	}
}

func newColRepl(t *testing.T, lines []string) *repl {
	t.Helper()
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	r := newRepl(lines, 80, 24)
	r.delim, r.quotes, r.headers = ',', true, true
	return r
}

func TestColumnsInsert(t *testing.T) {
	r := newColRepl(t, []string{"name,age", "alice,30", "bob,7"})
	if !r.structDispatch("ic 1") { // right of column 1
		t.Fatal("ic 1 should be a structural command")
	}
	want := []string{"name,,age", "alice,,30", "bob,,7"}
	if !reflect.DeepEqual(r.b.lines, want) {
		t.Fatalf("ic 1 -> %q, want %q", r.b.lines, want)
	}
	if !r.b.modified {
		t.Error("a column insert should mark the buffer modified")
	}
	// Undo reverts the whole op in one step.
	r.undoAtPrompt()
	if !reflect.DeepEqual(r.b.lines, []string{"name,age", "alice,30", "bob,7"}) {
		t.Errorf("undo of ic -> %q", r.b.lines)
	}
}

func TestColumnsInsertAppendAndPrepend(t *testing.T) {
	r := newColRepl(t, []string{"a,b", "c,d"})
	r.structDispatch("ic") // bare: append at the far right
	if !reflect.DeepEqual(r.b.lines, []string{"a,b,", "c,d,"}) {
		t.Fatalf("bare ic -> %q", r.b.lines)
	}
	r.structDispatch("insert column 0") // long form, prepend
	if !reflect.DeepEqual(r.b.lines, []string{",a,b,", ",c,d,"}) {
		t.Fatalf("insert column 0 -> %q", r.b.lines)
	}
}

func TestColumnsKillConfirmed(t *testing.T) {
	r := newColRepl(t, []string{"name,age,city", "alice,30,rome", "bob,7,pisa"})
	pr, pw, _ := os.Pipe()
	pw.WriteString("y\r")
	pw.Close()
	r.rd = &reader{in: pr}
	if !r.structDispatch("kc 2") {
		t.Fatal("kc 2 should be a structural command")
	}
	want := []string{"name,city", "alice,rome", "bob,pisa"}
	if !reflect.DeepEqual(r.b.lines, want) {
		t.Fatalf("kc 2 (confirmed) -> %q, want %q", r.b.lines, want)
	}
}

func TestColumnsKillCancelled(t *testing.T) {
	r := newColRepl(t, []string{"name,age", "alice,30"})
	pr, pw, _ := os.Pipe()
	pw.WriteString("n\r")
	pw.Close()
	r.rd = &reader{in: pr}
	r.structDispatch("kill column 2") // long form
	if !reflect.DeepEqual(r.b.lines, []string{"name,age", "alice,30"}) {
		t.Errorf("a cancelled kill must change nothing, got %q", r.b.lines)
	}
	if r.b.modified {
		t.Error("a cancelled kill must not mark the buffer modified")
	}
}

func TestColumnsKillBareErrors(t *testing.T) {
	r := newColRepl(t, []string{"a,b", "c,d"})
	r.structDispatch("kc") // no column number — destructive, must refuse
	if !reflect.DeepEqual(r.b.lines, []string{"a,b", "c,d"}) {
		t.Errorf("bare kc must change nothing, got %q", r.b.lines)
	}
	r.structDispatch("kc 9") // out of range
	if !reflect.DeepEqual(r.b.lines, []string{"a,b", "c,d"}) {
		t.Errorf("out-of-range kc must change nothing, got %q", r.b.lines)
	}
}

func TestColumnsQuotedRoundTrip(t *testing.T) {
	// A quoted field with an embedded delimiter must survive a column insert
	// verbatim — the raw-cell join keeps its quotes and inner comma intact.
	r := newColRepl(t, []string{`a,"b,c",d`})
	r.structDispatch("ic 2") // right of the quoted field
	if r.b.lines[0] != `a,"b,c",,d` {
		t.Fatalf("quoted round-trip -> %q, want %q", r.b.lines[0], `a,"b,c",,d`)
	}
}

func TestColumnsUnbalancedAborts(t *testing.T) {
	// With quotes on, a line that won't parse must abort the whole op untouched.
	r := newColRepl(t, []string{"a,b", `c,"unterminated`})
	before := append([]string(nil), r.b.lines...)
	r.structDispatch("ic 1")
	if !reflect.DeepEqual(r.b.lines, before) {
		t.Errorf("an unbalanced-quote line must abort the op, got %q", r.b.lines)
	}
	if r.b.modified {
		t.Error("an aborted column op must not mark the buffer modified")
	}
}

func TestColumnsDsvOnly(t *testing.T) {
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	r := newRepl([]string{"a,b", "c,d"}, 80, 24) // no delimiter set
	r.structDispatch("ic 1")
	if r.b.modified || !reflect.DeepEqual(r.b.lines, []string{"a,b", "c,d"}) {
		t.Errorf("columns outside dsv must be a no-op, got %q modified=%v", r.b.lines, r.b.modified)
	}
}

func TestColumnRuler(t *testing.T) {
	// Numbers sit at each column's left edge, padded to the column width plus the
	// gap so they head their columns. Widths [4,3,5], printable-delim gap 3.
	got := columnRuler([]int{4, 3, 5}, 3)
	want := "1      2     3" // "1"+3pad +3gap +"2"+2pad +3gap +"3"
	if got != want {
		t.Errorf("columnRuler = %q, want %q", got, want)
	}
}

// --- rows ------------------------------------------------------------------

func TestRowsInsert(t *testing.T) {
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	r := newRepl([]string{"one", "two", "three"}, 80, 24)
	if !r.structDispatch("ir 1") { // blank line after line 1
		t.Fatal("ir 1 should be a structural command")
	}
	want := []string{"one", "", "two", "three"}
	if !reflect.DeepEqual(r.b.lines, want) {
		t.Fatalf("ir 1 -> %q, want %q", r.b.lines, want)
	}
	if !r.b.modified {
		t.Error("a row insert should mark the buffer modified")
	}
	r.undoAtPrompt() // one step reverts it
	if !reflect.DeepEqual(r.b.lines, []string{"one", "two", "three"}) {
		t.Errorf("undo of ir -> %q", r.b.lines)
	}
}

func TestRowsInsertAppendPrependAndShape(t *testing.T) {
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	// Plain text: bare append adds an empty line at the end, 0 prepends.
	r := newRepl([]string{"a", "b"}, 80, 24)
	r.structDispatch("insert row") // bare append
	if !reflect.DeepEqual(r.b.lines, []string{"a", "b", ""}) {
		t.Fatalf("bare insert row -> %q", r.b.lines)
	}
	r.structDispatch("ir 0") // prepend
	if !reflect.DeepEqual(r.b.lines, []string{"", "a", "b", ""}) {
		t.Fatalf("ir 0 -> %q", r.b.lines)
	}
	// Delimited: the blank record carries one empty field per column so it aligns.
	d := newRepl([]string{"x,y,z", "1,2,3"}, 80, 24)
	d.delim, d.quotes, d.headers = ',', true, true
	d.structDispatch("ir 1")
	if d.b.lines[1] != ",," { // 3 empty fields
		t.Fatalf("delimited insert row shape -> %q, want %q", d.b.lines[1], ",,")
	}
}

func TestRowsKillSingleNoConfirm(t *testing.T) {
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	r := newRepl([]string{"one", "two", "three"}, 80, 24)
	r.structDispatch("kr 2") // a single line: no confirm
	if !reflect.DeepEqual(r.b.lines, []string{"one", "three"}) {
		t.Fatalf("kr 2 -> %q", r.b.lines)
	}
	r.undoAtPrompt()
	if !reflect.DeepEqual(r.b.lines, []string{"one", "two", "three"}) {
		t.Errorf("undo of kr -> %q", r.b.lines)
	}
}

func TestRowsKillRangeConfirmed(t *testing.T) {
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	r := newRepl([]string{"1", "2", "3", "4", "5"}, 80, 24)
	pr, pw, _ := os.Pipe()
	pw.WriteString("y\r")
	pw.Close()
	r.rd = &reader{in: pr}
	r.structDispatch("kill row 2.4") // a range: confirms, reuses parseAddress
	if !reflect.DeepEqual(r.b.lines, []string{"1", "5"}) {
		t.Fatalf("kill row 2.4 (confirmed) -> %q", r.b.lines)
	}
}

func TestRowsKillRangeCancelled(t *testing.T) {
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	r := newRepl([]string{"1", "2", "3", "4"}, 80, 24)
	pr, pw, _ := os.Pipe()
	pw.WriteString("n\r")
	pw.Close()
	r.rd = &reader{in: pr}
	r.structDispatch("kr 2.3")
	if !reflect.DeepEqual(r.b.lines, []string{"1", "2", "3", "4"}) {
		t.Errorf("a cancelled range kill must change nothing, got %q", r.b.lines)
	}
}

func TestRowsKillRefusesAll(t *testing.T) {
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	r := newRepl([]string{"only", "two"}, 80, 24)
	pr, pw, _ := os.Pipe()
	pw.WriteString("y\r")
	pw.Close()
	r.rd = &reader{in: pr}
	r.structDispatch("kr 1.2") // would empty the buffer — refused before the confirm
	if !reflect.DeepEqual(r.b.lines, []string{"only", "two"}) {
		t.Errorf("killing every line must be refused, got %q", r.b.lines)
	}
}

func TestRowsBareReportsCount(t *testing.T) {
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	r := newRepl([]string{"a", "b", "c"}, 80, 24)
	r.last = &block{start: 1, count: 3}
	if !r.structDispatch("rows") { // bare noun reports state
		t.Fatal("bare rows should be a structural command")
	}
	if r.last != nil {
		t.Error("a bare rows report should clear r.last")
	}
}

func TestStructDispatchFallThrough(t *testing.T) {
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	r := newRepl([]string{"hello"}, 80, 24)
	for _, s := range []string{"csv", "clear", "category", "insertion", "killer", "5.10", "5"} {
		if r.structDispatch(s) {
			t.Errorf("%q should not match structDispatch", s)
		}
	}
}

// TestPrintColumnsRendersRuler captures the full columns view and checks the
// faint index ruler leads the grid, above the header, with a number per column.
func TestPrintColumnsRendersRuler(t *testing.T) {
	var buf strings.Builder
	old := screen
	screen = &buf
	t.Cleanup(func() { screen = old })
	r := newRepl([]string{"name,age", "Ada,36", "Linus,54"}, 80, 24)
	r.delim, r.quotes, r.headers = ',', true, true
	r.printColumns(1, 3)
	clean := regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`).ReplaceAllString(buf.String(), "")
	rows := strings.Split(strings.ReplaceAll(clean, "\r", ""), "\n")
	// Find the ruler row (the one whose trimmed content starts "1") and the header.
	rulerAt, headerAt := -1, -1
	for i, ln := range rows {
		tr := strings.TrimSpace(ln)
		if rulerAt < 0 && strings.HasPrefix(tr, "1 ") && strings.Contains(tr, "2") {
			rulerAt = i
		}
		if strings.Contains(ln, "name") && strings.Contains(ln, "age") {
			headerAt = i
		}
	}
	if rulerAt < 0 {
		t.Fatalf("no index ruler found in:\n%s", clean)
	}
	if headerAt < 0 || rulerAt >= headerAt {
		t.Errorf("ruler (row %d) must sit above the header (row %d)", rulerAt, headerAt)
	}
}

// A splitLine undo whose block has shrunk to show only the upper half must not
// collapse the editor block to zero lines (which later panicked on a move via
// clamp(_, 0, -1)). It hands off to the prompt-level undo instead.
func TestUndoDoesNotCollapseBlock(t *testing.T) {
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	r := newRepl([]string{"foo"}, 80, 24)
	e := &editor{r: r, start: 1, count: 1, cy: 0, cx: 1}
	e.splitLine() // "f" / "oo"; count -> 2, pushes a split undo (lineDelta -1)
	if e.count != 2 || len(r.b.lines) != 2 {
		t.Fatalf("split: count=%d lines=%d, want 2/2", e.count, len(r.b.lines))
	}
	// Simulate reprinting only the upper half and climbing back into a 1-line
	// block sitting on the split point; the split-undo entry is still queued.
	e.count, e.cy, e.cx = 1, 0, 0
	if act := e.undo(); act != actUndo {
		t.Fatalf("undo of a split into a 1-line block should hand off (actUndo), got %v", act)
	}
	if e.count != 1 {
		t.Fatalf("block must not collapse to zero: count=%d, want 1", e.count)
	}
	// The handoff path applies the queued entry correctly, rejoining the line.
	r.undoAtPrompt()
	if len(r.b.lines) != 1 || r.b.lines[0] != "foo" {
		t.Fatalf("prompt undo should rejoin to %q (1 line), got %q (%d lines)", "foo", r.b.lines[0], len(r.b.lines))
	}
}

// A zero-width match (e.g. the ^ anchor) replaced with empty text once looped
// forever: the next-match scan restarted at the same rune. replaceNext must
// step past a zero-width, no-insert replacement so the stepping terminates.
func TestReplaceNextZeroWidthTerminates(t *testing.T) {
	screen = io.Discard
	t.Cleanup(func() { screen = os.Stdout })
	r := newRepl([]string{"abc"}, 80, 24)
	r.replaceDispatch("replace /^//") // old "^" (zero-width), new "" (empty)
	steps := 0
	for r.search != nil && steps < 10 {
		r.replaceDispatch("replace")
		steps++
	}
	if r.search != nil {
		t.Fatalf("zero-width empty replacement should terminate, still armed after %d steps: %+v", steps, r.search)
	}
}
