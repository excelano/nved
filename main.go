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

	// Field-layer DSV state — view-level, never touches the buffer. delim is the
	// master switch: zero means plain text (no fields, no alignment), any other
	// rune turns column rendering on. quotes and headers are independent knobs;
	// the csv/tsv/asv presets set all three at once. See dsv.go.
	delim   rune
	quotes  bool
	headers bool

	// wrap controls how a too-wide plain-text line is shown: on (the default) word-
	// wraps it onto continuation rows; off renders one row per line and pans it
	// sideways under the cursor, the same windowing the aligned DSV view uses. It is
	// the text-mode counterpart of the aligned view's always-on horizontal pan, and
	// is independent of the DSV layer — orthogonal to delim/quotes/headers.
	wrap bool

	// lastAligned records whether the last printed block rendered as aligned
	// columns (delimiter set and every line parsed) rather than raw text. The
	// editor reads it to choose aligned vs. raw geometry on a climb, and the climb
	// gate uses it so a delimiter set on an un-alignable block (a multi-line
	// quoted field shown raw) stays read-only rather than climbing into a view
	// whose geometry the editor can't reproduce.
	lastAligned bool

	// search is the armed find/replace, nil when none. It survives between
	// commands so `find next` can step from the current match and the print path
	// can highlight it; see search.go.
	search *searchState

	// pendingLine pre-fills the next command prompt, the throwaway armed-`next`
	// seed: after a find lands a match it is set to "find next", so the next prompt
	// reads "find next" with the cursor after it and Enter steps to the following
	// match. The chord seeds it too. Consumed (cleared) the moment it is echoed.
	pendingLine string
}

// block records a printed range so a later climb knows where on screen the
// editable lines are: the 1-based buffer line at the top and how many rows.
type block struct {
	start int
	count int
}

// cmdResult is what one trip through readCommand produces.
type cmdResult struct {
	kind    cmdKind
	line    string // the typed command, when kind == cmdSubmit
	climb   key    // the key that triggered a climb, when kind == cmdClimb
	toMatch bool   // the climb came off an armed search seed: land on the match
}

type cmdKind int

const (
	cmdSubmit   cmdKind = iota // line holds a command to dispatch (may be empty)
	cmdClimb                   // climb into the last block, entering on climb
	cmdPageUp                  // reprint the screenful above the block
	cmdPageDown                // reprint the screenful below the block
	cmdUndo                    // Ctrl+U at the prompt: undo the last edit
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

	// A "+SPEC" argument (vim/less style) opens straight to a range: SPEC is any
	// prompt command — "+3.10", "+tail", "+$-20.$" — run once on startup so the
	// block is sitting above the prompt, exactly as if it had been typed. The "+"
	// marks it unambiguously, so it works in any position and never collides with
	// a file named like an address.
	var name, startSpec string
	for _, a := range args {
		if len(a) > 1 && a[0] == '+' {
			startSpec = a[1:]
		} else if name == "" {
			name = a
		}
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

	// On Windows, switch the console into ANSI mode so the escape sequences nved
	// emits are interpreted rather than printed. A no-op on Unix.
	enableVirtualTerminal()

	switch {
	case name == "":
		emit("nved — empty buffer (no file)\n")
	case newFile:
		emitf("%s: new file\n", name)
	default:
		emitf("%s: %d lines\n", name, len(b.lines))
	}

	w, h := termSize(fd)
	r := &repl{b: b, rd: newReader(), termW: w, termH: h, wrap: true}
	if startSpec != "" {
		if r.dispatch(startSpec) {
			return
		}
	}
	r.run()
}

// printUsage writes the command-line help shown by --help / -h. The in-editor
// command and key reference lives behind the `h` command at the prompt.
func printUsage() {
	fmt.Print(`Usage: nved [+spec] [file]

A small REPL-flavored terminal text editor. Print a range of lines by number,
climb into the printed block with Up / Left, and edit it in place. With no file
it opens an empty unnamed buffer.

A +spec argument opens straight to a range — it is any prompt command, run once
on startup:
  nved +42 notes.txt        open with line 42 printed
  nved +10.30 notes.txt     ... lines 10 through 30
  nved +tail notes.txt      ... the last screenful, ready to climb into

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
			switch r.edit(res.climb, res.toMatch) {
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
			case actUndo:
				r.undoAtPrompt()
			}
		case cmdPageUp:
			r.pageUp()
		case cmdPageDown:
			r.pageDown()
		case cmdUndo:
			r.undoAtPrompt()
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
// Page-Up/Page-Down return a page; every other navigation key is swallowed. On
// an empty line Ctrl+U undoes the last edit, the same chord as in the editor;
// with text typed it is swallowed so a half-written command is not lost.
func (r *repl) readCommand() cmdResult {
	var line []rune
	// armed marks the line as the throwaway "<verb> next" search seed, pre-filled
	// from pendingLine and not yet touched. While armed a navigation key acts as if
	// the line were empty — a climb lands on the highlighted match (toMatch) and
	// paging scrolls — since the seed is UI, not a half-typed command. The first
	// edit (a rune or backspace) demotes it to ordinary typed text.
	armed := false
	if r.pendingLine != "" {
		line = []rune(r.pendingLine)
		out(string(line))
		r.pendingLine = ""
		armed = true
	}
	// nav reports whether a climb/page should fire now: an empty line, or the
	// untouched armed seed.
	nav := func() bool { return len(line) == 0 || armed }
	climbable := func() bool { return r.last != nil && (r.delim == 0 || r.lastAligned) }
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
		case keyCtrlF:
			// Seed a fresh search: replace whatever is on the line with "find ", ready
			// for the pattern. Ctrl+F always starts over, so it works mid-line too.
			out("\r" + csiEL + prompt + "find ")
			line = []rune("find ")
			armed = false
		case keyCtrlR:
			// The replace twin of Ctrl+F: seed "replace ", ready for /old/new/.
			out("\r" + csiEL + prompt + "replace ")
			line = []rune("replace ")
			armed = false
		case keyCtrlU:
			if len(line) == 0 {
				out("\r\n")
				return cmdResult{kind: cmdUndo}
			}
		case keyEsc:
			// Clear the command line back to a bare prompt — the design's way out of
			// the armed seed (Backspace, held, gets there too).
			out("\r" + csiEL + prompt)
			line = nil
			armed = false
		case keyEnter:
			out("\r\n")
			return cmdResult{kind: cmdSubmit, line: string(line)}
		case keyBackspace:
			if len(line) > 0 {
				line = line[:len(line)-1]
				out("\b \b")
				armed = false
			}
		case keyRune:
			line = append(line, k.r)
			out(string(k.r))
			armed = false
		case keyUp, keyLeft:
			// Climb into the block to edit it: plain text always, or an aligned DSV
			// block (the editor reproduces the aligned geometry). A delimiter set on
			// an un-alignable block — a multi-line quoted field shown raw — stays
			// read-only, since the on-screen raw view isn't what the editor draws.
			// Off an armed seed the climb lands on the search match (toMatch).
			if nav() && climbable() {
				return cmdResult{kind: cmdClimb, climb: k, toMatch: armed}
			}
		case keyHome:
			if k.ctrl && nav() && climbable() {
				return cmdResult{kind: cmdClimb, climb: k, toMatch: armed}
			}
		case keyPageUp:
			// Only page when there is a line above the block to bring on screen;
			// otherwise swallow the key in place, like the other nav keys, so the
			// prompt line isn't disturbed.
			if nav() && r.last != nil && r.last.start > 1 {
				return cmdResult{kind: cmdPageUp}
			}
		case keyPageDown:
			if nav() && r.last != nil && r.last.start+r.last.count <= len(r.b.lines) {
				return cmdResult{kind: cmdPageDown}
			}
		}
	}
}
