// Command nved is a REPL-flavored terminal text editor — "new ved".
//
// Slice 2: the editor core. On top of slice 1's command REPL it adds climbing
// into the most-recently-printed block to edit it in place — cursor movement,
// insert, Enter-split, Backspace/Delete-join — with ANSI block redraw over the
// real (scrolling) terminal rather than an alternate screen.
package main

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

const prompt = "> " // a plain chevron, like the Claude Code prompt

// Populated at build time via -ldflags; "dev" for a plain `go build`.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// repl is the read-eval-print loop and the small amount of state it carries
// between commands: the buffer, the key reader, and a record of the block last
// printed (nil when the line below the prompt is no longer that block, so a
// climb key has nothing valid to climb into).
type repl struct {
	b     *buffer
	rd    *reader
	last  *block
	termW int // terminal width, refreshed when a block is printed
	termH int // terminal height, used to size a page to one screenful
}

// block records a printed range so a later climb knows where on screen the
// editable lines are: the 1-based buffer line at the top and how many rows.
type block struct {
	start int
	count int
}

// cmdResult is what one trip through readCommand produces.
type cmdResult struct {
	kind  cmdKind
	line  string // the typed command, when kind == cmdSubmit
	climb key    // the key that triggered a climb, when kind == cmdClimb
}

type cmdKind int

const (
	cmdSubmit   cmdKind = iota // line holds a command to dispatch (may be empty)
	cmdClimb                   // climb into the last block, entering on climb
	cmdPageUp                  // reprint the screenful above the block
	cmdPageDown                // reprint the screenful below the block
	cmdQuit                    // Ctrl+C or end of input
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "--version", "-V":
			fmt.Printf("nved %s (%s, %s)\n", version, commit, date)
			return
		case "--help", "-h":
			printUsage()
			return
		}
	}

	var name string
	if len(args) > 0 {
		name = args[0]
	}

	b, newFile, err := openBuffer(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nved: %v\n", err)
		os.Exit(1)
	}

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		fmt.Fprintln(os.Stderr, "nved: stdin is not a terminal")
		os.Exit(1)
	}
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nved: cannot enter raw mode: %v\n", err)
		os.Exit(1)
	}
	defer term.Restore(fd, oldState)

	switch {
	case name == "":
		emit("nved — empty buffer (no file)\n")
	case newFile:
		emitf("%s: new file\n", name)
	default:
		emitf("%s: %d lines\n", name, len(b.lines))
	}

	w, h := termSize(fd)
	r := &repl{b: b, rd: newReader(), termW: w, termH: h}
	r.run()
}

// printUsage writes the command-line help shown by --help / -h. The in-editor
// command and key reference lives behind the `h` command at the prompt.
func printUsage() {
	fmt.Print(`Usage: nved [file]

A small REPL-flavored terminal text editor. Print a range of lines by number,
climb into the printed block with Up / Left, and edit it in place. With no file
it opens an empty unnamed buffer.

Flags:
  -h, --help      Show this help
  -V, --version   Show version

At the prompt, type h for the command and key reference.
`)
}

// termSize reports the terminal's column and row count, falling back to 80×24 if
// the size can't be read.
func termSize(fd int) (w, h int) {
	w, h, err := term.GetSize(fd)
	if err != nil || w <= 0 || h <= 0 {
		return 80, 24
	}
	return w, h
}

// refreshSize re-reads the terminal size, so a window resize between commands is
// picked up the next time a block is printed. (Mid-edit resize — SIGWINCH — is a
// later slice.)
func (r *repl) refreshSize() { r.termW, r.termH = termSize(int(os.Stdin.Fd())) }

// run drives the loop: print the prompt, read a command, and either dispatch
// it, climb into the last block, or quit.
func (r *repl) run() {
	for {
		out(prompt)
		res := r.readCommand()
		switch res.kind {
		case cmdQuit:
			return
		case cmdClimb:
			switch r.edit(res.climb) {
			case actPageUp:
				r.pageUp()
			case actPageDown:
				r.pageDown()
			case actSave:
				if r.dispatch("s") {
					return
				}
			case actExit:
				if r.dispatch("x") {
					return
				}
			}
		case cmdPageUp:
			r.pageUp()
		case cmdPageDown:
			r.pageDown()
		case cmdSubmit:
			if r.dispatch(res.line) {
				return
			}
		}
	}
}

// readCommand reads one command line, echoing as it goes (ECHO is off in raw
// mode). Ctrl+S and Ctrl+X resolve to their command strings so the chord and
// the typed word share one dispatch path. On an empty command line with a block
// still below the prompt, a climb key (Up, Left, Ctrl+Home) returns a climb and
// Page-Up/Page-Down return a page; every other navigation key is swallowed.
func (r *repl) readCommand() cmdResult {
	var line []rune
	for {
		k, ok := r.rd.readKey()
		if !ok {
			out("\r\n")
			return cmdResult{kind: cmdQuit}
		}
		switch k.kind {
		case keyCtrlC:
			out("\r\n")
			return cmdResult{kind: cmdQuit}
		case keyCtrlS:
			out("\r\n")
			return cmdResult{kind: cmdSubmit, line: "s"}
		case keyCtrlX:
			out("\r\n")
			return cmdResult{kind: cmdSubmit, line: "x"}
		case keyEnter:
			out("\r\n")
			return cmdResult{kind: cmdSubmit, line: string(line)}
		case keyBackspace:
			if len(line) > 0 {
				line = line[:len(line)-1]
				out("\b \b")
			}
		case keyRune:
			line = append(line, k.r)
			out(string(k.r))
		case keyUp, keyLeft:
			if len(line) == 0 && r.last != nil {
				return cmdResult{kind: cmdClimb, climb: k}
			}
		case keyHome:
			if k.ctrl && len(line) == 0 && r.last != nil {
				return cmdResult{kind: cmdClimb, climb: k}
			}
		case keyPageUp:
			// Only page when there is a line above the block to bring on screen;
			// otherwise swallow the key in place, like the other nav keys, so the
			// prompt line isn't disturbed.
			if len(line) == 0 && r.last != nil && r.last.start > 1 {
				return cmdResult{kind: cmdPageUp}
			}
		case keyPageDown:
			if len(line) == 0 && r.last != nil && r.last.start+r.last.count <= len(r.b.lines) {
				return cmdResult{kind: cmdPageDown}
			}
		}
	}
}
