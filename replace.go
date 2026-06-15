package main

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The substitution layer, built on the search engine in search.go. replace is
// find's twin: same RE2 engine, same highlight, same armed-`next` interaction.
// Two modes share the verb. The stepping mode is preview-first — `replace /o/n/`
// highlights the first OLD match UNCHANGED, and each `replace next` replaces the
// highlighted one and advances — so you see before you change. The all-at-once
// mode, `replace all /o/n/`, replaces every match in the buffer in a single
// undoable run. Scope is buffer-wide throughout (find's twin); a range-scoped
// replace would be an additive mode, not the default.

// replaceDispatch handles the replace family and the short forms rn / ra. It
// reports whether s was a replace command so dispatch falls through when it
// isn't, and manages r.last itself (a successful replace prints a block).
func (r *repl) replaceDispatch(s string) bool {
	switch {
	case s == "rn": // short for replace next
		r.replaceNext()
		return true
	case s == "ra": // short for replace all, but with no spec
		r.startReplaceSpec("", true)
		return true
	}
	if spec, ok := matchVerb(s, "ra"); ok { // ra /old/new/
		r.startReplaceSpec(spec, true)
		return true
	}
	rest, ok := cutReplaceVerb(s)
	if !ok {
		return false
	}
	switch strings.TrimSpace(rest) {
	case "":
		r.reportReplace()
	case "next":
		r.replaceNext()
	default:
		// A leading `all` keyword switches to all-at-once; otherwise it is the
		// preview-first stepping spec. `all` is unambiguous: a real spec begins with
		// a non-alphanumeric delimiter, so a leading alphabetic word can only be the
		// keyword.
		if spec, ok := cutAllKeyword(rest); ok {
			r.startReplaceSpec(spec, true)
		} else {
			r.startReplaceSpec(rest, false)
		}
	}
	return true
}

// cutReplaceVerb matches the replace verb in either long or short form, allowing
// the delimiter to abut the verb (the no-space `r/old/new/` ergonomic). It tries
// the long verb first so `replace` doesn't read as `r` + `eplace`.
func cutReplaceVerb(s string) (rest string, ok bool) {
	if rest, ok := matchVerb(s, "replace"); ok {
		return rest, true
	}
	return matchVerb(s, "r")
}

// matchVerb reports whether s is verb followed by a space, a non-alphanumeric
// delimiter, or nothing at all (the bare verb), and returns the untrimmed
// remainder. A verb butted against a letter or digit — "rationale" against "r",
// "rn" against "r" — does NOT match, so unrelated words and the other short forms
// fall through cleanly.
func matchVerb(s, verb string) (rest string, ok bool) {
	if s == verb {
		return "", true
	}
	r, found := strings.CutPrefix(s, verb)
	if !found {
		return "", false
	}
	first, _ := utf8.DecodeRuneInString(r)
	if first == ' ' || first == '\t' || isDelim(first) {
		return r, true
	}
	return "", false
}

// cutAllKeyword strips a leading `all` keyword (after optional whitespace) and
// returns the remaining spec, requiring `all` to be followed by whitespace, a
// delimiter, or end — so it is the keyword, not the head of a pattern.
func cutAllKeyword(rest string) (spec string, ok bool) {
	t := strings.TrimLeft(rest, " \t")
	r, found := strings.CutPrefix(t, "all")
	if !found {
		return "", false
	}
	first, _ := utf8.DecodeRuneInString(r)
	if r == "" || first == ' ' || first == '\t' || isDelim(first) {
		return r, true
	}
	return "", false
}

// isDelim reports whether r may serve as the replace delimiter: anything that is
// not a letter or digit. The guard turns the no-space word forms (`replaced`,
// `rn`) into non-commands and keeps "any delimiter" sane.
func isDelim(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }

// parseReplaceSpec parses an ed-style /old/new/ spec: skip optional leading
// whitespace, take the first character as the delimiter (which must be
// non-alphanumeric), then split the body into old and new. A trailing delimiter
// is allowed; any text past it is rejected with a nudge toward `replace all`,
// since the global flag is a keyword here, not a /g suffix.
func parseReplaceSpec(spec string) (old, repl string, err error) {
	s := strings.TrimLeft(spec, " \t")
	if s == "" {
		return "", "", fmt.Errorf("replace needs /old/new/ (any non-letter delimiter)")
	}
	d, sz := utf8.DecodeRuneInString(s)
	if !isDelim(d) {
		return "", "", fmt.Errorf("replace delimiter must be non-alphanumeric, not %q", d)
	}
	parts := strings.SplitN(s[sz:], string(d), 3)
	if len(parts) < 2 {
		return "", "", fmt.Errorf("replace needs a closing %[1]q: %[1]cold%[1]cnew%[1]c", d)
	}
	if parts[0] == "" {
		return "", "", fmt.Errorf("replace old pattern is empty")
	}
	if len(parts) == 3 && parts[2] != "" {
		return "", "", fmt.Errorf("unexpected text after the closing delimiter — for global use %q", "replace all "+string(d)+parts[0]+string(d)+parts[1]+string(d))
	}
	return parts[0], parts[1], nil
}

// startReplaceSpec parses and compiles a spec, then either replaces all matches
// at once (global) or arms preview-first stepping on the first match. A bad spec
// or pattern is reported and clears the block; no match disarms any prior search.
func (r *repl) startReplaceSpec(spec string, global bool) {
	old, repl, err := parseReplaceSpec(spec)
	if err != nil {
		emitf("nved: %v\n", err)
		r.last = nil
		return
	}
	re, err := regexp.Compile(old)
	if err != nil {
		emitf("nved: bad pattern: %v\n", err)
		r.last = nil
		return
	}
	if global {
		r.replaceAll(re, old, repl)
		return
	}
	line, lo, hi, _, found := searchForward(r.b.lines, re, 0, 0)
	if !found {
		emitf("replace: no match for %q\n", old)
		r.search = nil
		r.last = nil
		return
	}
	r.search = &searchState{re: re, pat: old, line: line, lo: lo, hi: hi, isRepl: true, repl: repl}
	r.showMatch()
	r.pendingLine = "replace next"
}

// replaceNext applies the highlighted OLD match — the pending candidate — then
// advances the highlight to the next match, arming another step. With no more
// matches it reports the running tally and reprints the final state. Each step is
// its own undo entry, so Ctrl+U peels the replacements back one at a time. The
// next scan runs forward from just past the inserted text and does not wrap: the
// first candidate was the buffer's first match, so forward covers the rest.
func (r *repl) replaceNext() {
	cur := r.search
	if cur == nil || !cur.isRepl {
		emit("replace: nothing armed — replace /old/new/ first\n")
		r.last = nil
		return
	}
	li := cur.line
	rs := []rune(r.b.lines[li])
	matched := string(rs[cur.lo:cur.hi])
	newText := cur.re.ReplaceAllString(matched, cur.repl)
	oldLine, preMod := r.b.lines[li], r.b.modified
	r.b.lines[li] = string(rs[:cur.lo]) + newText + string(rs[cur.hi:])
	r.b.modified = true
	r.b.pushUndo(undoEntry{
		apply: func(b *buffer) { b.lines[li] = oldLine; b.modified = preMod },
		line:  li, col: cur.lo,
	})
	cur.count++

	line, lo, hi, wrapped, found := searchForward(r.b.lines, cur.re, li, cur.lo+len([]rune(newText)))
	if found && !wrapped {
		cur.line, cur.lo, cur.hi = line, lo, hi
		r.showMatch()
		r.pendingLine = "replace next"
		return
	}
	n := cur.count
	r.search = nil
	emit(faint(fmt.Sprintf("replace: replaced %d", n)) + "\n")
	r.printLines(li+1, len(r.b.lines)) // show the final state, no highlight now
}

// replaceAll rewrites every match in the buffer in one pass and records a single
// undo entry restoring all the lines it touched. It reports the count and
// reprints from the first changed line. No match disarms and clears the block.
func (r *repl) replaceAll(re *regexp.Regexp, old, repl string) {
	count, first := 0, -1
	changed := map[int]string{}
	for i, line := range r.b.lines {
		hits := len(re.FindAllStringIndex(line, -1))
		if hits == 0 {
			continue
		}
		if first < 0 {
			first = i
		}
		changed[i] = line
		r.b.lines[i] = re.ReplaceAllString(line, repl)
		count += hits
	}
	if count == 0 {
		emitf("replace: no match for %q\n", old)
		r.search = nil
		r.last = nil
		return
	}
	preMod := r.b.modified
	r.b.modified = true
	r.b.pushUndo(undoEntry{
		apply: func(b *buffer) {
			for i, prev := range changed {
				b.lines[i] = prev
			}
			b.modified = preMod
		},
		line: first, col: 0,
	})
	r.search = nil
	emit(faint(fmt.Sprintf("replace: replaced %d", count)) + "\n")
	r.printLines(first+1, len(r.b.lines))
}

// reportReplace is the bare-verb state line: the active substitution, or that
// there is none. Like the other state reports it clears r.last.
func (r *repl) reportReplace() {
	if r.search == nil || !r.search.isRepl {
		emit("replace: nothing armed\n")
	} else {
		emitf("replace: %q -> %q\n", r.search.pat, r.search.repl)
	}
	r.last = nil
}
