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

// save writes the buffer to its file, one line per record with a trailing
// newline, and returns the number of lines written.
func (b *buffer) save() (int, error) {
	var sb strings.Builder
	for _, ln := range b.lines {
		sb.WriteString(ln)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(b.name, []byte(sb.String()), 0644); err != nil {
		return 0, err
	}
	b.modified = false
	return len(b.lines), nil
}

// splitLines turns raw file bytes into lines, dropping a single trailing
// newline so a newline-terminated file does not gain a phantom empty last
// line. Empty input becomes one empty line, keeping the buffer non-empty.
func splitLines(data []byte) []string {
	if len(data) == 0 {
		return []string{""}
	}
	s := strings.TrimSuffix(string(data), "\n")
	return strings.Split(s, "\n")
}
