package main

import (
	"os"
	"strings"
)

// buffer is the in-memory text being edited. It always holds at least one
// line, so an empty file and a brand-new file both present as a single empty
// line 1 — which unifies empty-buffer entry with the address clamp rule.
type buffer struct {
	lines     []string
	name      string // file name, "" when unnamed
	modified  bool
	exitArmed bool        // set after the first x on a dirty buffer (warn-twice)
	undos     []undoEntry // edit history, newest last; see undo.go

	// recSep is the record separator: how bytes split into lines and how lines
	// join back on save. The zero value means newline (see sep), so plain text
	// and the test buffers need not set it; rows record switches it to the ASCII
	// record separator (0x1E). This is the record layer — see dsv.go.
	recSep rune
}

// sep is the buffer's record separator, defaulting an unset (zero) recSep to
// newline so a freshly opened or test-built buffer behaves as ordinary text.
func (b *buffer) sep() rune {
	if b.recSep == 0 {
		return '\n'
	}
	return b.recSep
}

// reline reinterprets the buffer's bytes under a new record separator: rejoin the
// current lines on the old separator to recover the byte stream, then re-split on
// the new one. It is lossless and reversible — relining back to the old separator
// restores the exact lines. Because it redefines what a line is, it behaves like
// reopening the file: the undo stack, which describes the old line structure, is
// cleared. The caller resets the on-screen block.
func (b *buffer) reline(newSep rune) {
	joined := strings.Join(b.lines, string(b.sep()))
	b.lines = splitRecords(joined, newSep)
	b.recSep = newSep
	b.undos = nil
}

// openBuffer loads name into a buffer. The bool return is true for the
// new-file case — the file does not exist yet, so we open an empty buffer that
// remembers the name and create nothing on disk until the first save. An empty
// name yields an unnamed empty buffer.
func openBuffer(name string) (*buffer, bool, error) {
	if name == "" {
		return &buffer{lines: []string{""}}, false, nil
	}
	data, err := os.ReadFile(name)
	if err != nil {
		if os.IsNotExist(err) {
			return &buffer{lines: []string{""}, name: name}, true, nil
		}
		return nil, false, err
	}
	return &buffer{lines: splitLines(data), name: name}, false, nil
}

// save writes the buffer to its file, one record per line joined by the buffer's
// record separator (newline by default, the ASCII record separator under rows
// record), each record followed by a trailing separator, and returns the number
// of lines written.
func (b *buffer) save() (int, error) {
	sep := string(b.sep())
	var sb strings.Builder
	for _, ln := range b.lines {
		sb.WriteString(ln)
		sb.WriteString(sep)
	}
	if err := os.WriteFile(b.name, []byte(sb.String()), 0644); err != nil {
		return 0, err
	}
	b.modified = false
	return len(b.lines), nil
}

// splitLines turns raw file bytes into lines on newline boundaries — the layout
// every file opens with, before any rows command.
func splitLines(data []byte) []string {
	return splitRecords(string(data), '\n')
}

// splitRecords splits s into records on sep, dropping a single trailing
// separator so a separator-terminated stream does not gain a phantom empty last
// record. Empty input becomes one empty record, keeping the buffer non-empty.
func splitRecords(s string, sep rune) []string {
	if s == "" {
		return []string{""}
	}
	s = strings.TrimSuffix(s, string(sep))
	return strings.Split(s, string(sep))
}
