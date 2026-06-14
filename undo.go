package main

// undoEntry reverses a single edit. The stack lives on the buffer rather than on
// an editing session, so undo survives climbing out of a block and works at the
// command prompt as well as while editing.
//
// apply mutates the buffer back to its prior state. It captures ABSOLUTE buffer
// line indices when the edit is recorded — not session-relative (cy, cx) offsets
// from the block top — so it stays valid no matter which block (if any) is on
// screen when it runs, and restores the modified flag the edit set. line and col
// are the 0-based buffer line and rune column the edit began at, where a repaint
// should land: the editor maps them back to a cursor, and a prompt-level undo
// reprints that line. lineDelta is the change in buffer line count apply causes
// (+1 when it re-splits a joined line, -1 when it re-joins a split, 0 for an
// in-line character edit); the editor adds it to the on-screen block's height.
type undoEntry struct {
	apply     func(*buffer)
	line      int
	col       int
	lineDelta int
}

// pushUndo records an inverse as the newest entry.
func (b *buffer) pushUndo(e undoEntry) { b.undos = append(b.undos, e) }

// peekUndo returns the newest inverse without removing it; ok is false when the
// stack is empty. The editor peeks first to decide whether the edit falls inside
// the current block before committing to pop it.
func (b *buffer) peekUndo() (undoEntry, bool) {
	n := len(b.undos)
	if n == 0 {
		return undoEntry{}, false
	}
	return b.undos[n-1], true
}

// popUndo removes and returns the newest inverse; ok is false when the stack is
// empty.
func (b *buffer) popUndo() (undoEntry, bool) {
	n := len(b.undos)
	if n == 0 {
		return undoEntry{}, false
	}
	e := b.undos[n-1]
	b.undos = b.undos[:n-1]
	return e, true
}
