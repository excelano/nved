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
