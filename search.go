package main

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// The search layer: find a regular-expression match, walk the buffer to the next
// one, and reprint the screenful around it so it can be climbed into. A search is
// armed once started and remembered on the repl, so `find next` (and, later,
// `replace next`) can step from the current match without retyping the pattern.
// Matches are buffer-wide and wrap at the end — find IS navigation here, the
// "dial in by content" half of the round-out (climb in by line number is the
// other). RE2 via the standard library, so a bad pattern is reported, never a
// hang.

// searchState carries an armed search across commands. re is the compiled
// pattern and pat its source text (for the state report); line/lo/hi locate the
// current match — a 0-based buffer line and the rune range [lo,hi) within that
// line's raw text, the same raw-rune coordinates the cursor uses, so a later
// climb can land on it and the print path can highlight it.
type searchState struct {
	re  *regexp.Regexp
	pat string

	line   int // 0-based buffer line of the current match
	lo, hi int // rune range [lo,hi) of the match within that line
}

// findDispatch handles the search command family: `find`/`f` with a pattern
// starts a search, `find next`/`f next` steps to the following match, and a bare
// verb reports the active search (the standing bare-reports-state convention). It
// reports whether s was a find command so the main dispatch falls through to
// address parsing when it isn't. Unlike the state-reporting commands it does NOT
// blanket-clear r.last: a successful find prints a climbable block and must keep
// it, so each branch manages r.last itself.
func (r *repl) findDispatch(s string) bool {
	rest, ok := cutVerb(s, "find", "f")
	if !ok {
		return false
	}
	switch arg := strings.TrimSpace(rest); arg {
	case "":
		r.reportSearch()
	case "next":
		r.findNext()
	default:
		r.startFind(arg)
	}
	return true
}

// cutVerb matches s against any of the verbs — exactly, or as a "verb " prefix —
// and returns the untrimmed remainder. A bare word like "format" matches neither
// "find" nor "f", so dispatch falls through; the remainder is left untrimmed so a
// caller that cares about the first character after the verb (replace's delimiter)
// sees it verbatim. Longer verbs should be listed first so they win.
func cutVerb(s string, verbs ...string) (rest string, ok bool) {
	for _, v := range verbs {
		if s == v {
			return "", true
		}
		if r, found := strings.CutPrefix(s, v+" "); found {
			return r, true
		}
	}
	return "", false
}

// startFind compiles pat and jumps to the first match anywhere in the buffer,
// scanning from the top. A bad pattern is reported and clears the block; no match
// disarms any prior search. On a hit it records the match and reprints around it.
func (r *repl) startFind(pat string) {
	re, err := regexp.Compile(pat)
	if err != nil {
		emitf("nved: bad pattern: %v\n", err)
		r.last = nil
		return
	}
	line, lo, hi, _, found := searchForward(r.b.lines, re, 0, 0)
	if !found {
		emitf("find: no match for %q\n", pat)
		r.search = nil
		r.last = nil
		return
	}
	r.search = &searchState{re: re, pat: pat, line: line, lo: lo, hi: hi}
	r.showMatch()
}

// findNext steps to the match after the current one, wrapping to the top of the
// buffer at the end. With no active search it says so. The scan starts just past
// the current match — at its end, bumped one rune past a zero-width match so a
// pattern that can match empty can't stall — so the same position is never
// returned twice in a row unless it is the only match. A wrap prints a faint
// notice above the reprinted block, where it becomes scrollback and leaves the
// block itself directly above the prompt and still climbable.
func (r *repl) findNext() {
	if r.search == nil {
		emit("find: no active search — find <pattern> first\n")
		r.last = nil
		return
	}
	cur := r.search
	from := cur.hi
	if cur.hi == cur.lo { // zero-width match: step past it so the scan progresses
		from = cur.lo + 1
	}
	line, lo, hi, wrapped, found := searchForward(r.b.lines, cur.re, cur.line, from)
	if !found {
		emitf("find: no match for %q\n", cur.pat) // the buffer changed out from under us
		r.last = nil
		return
	}
	cur.line, cur.lo, cur.hi = line, lo, hi
	if wrapped {
		emit(faint("find: wrapped to top") + "\n")
	}
	r.showMatch()
}

// reportSearch is the bare-verb state line: the active pattern, or that there is
// none. Like the other state reports it clears r.last — the report sits between
// any printed block and the prompt, so a climb key would no longer land right.
func (r *repl) reportSearch() {
	if r.search == nil {
		emit("find: no active search\n")
	} else {
		emitf("find: %q\n", r.search.pat)
	}
	r.last = nil
}

// showMatch reprints the screenful holding the current match so it can be climbed
// into. When the match already sits within the block on screen the same range is
// reprinted in place — stepping through several matches on one screenful doesn't
// jump the view — otherwise the match line leads a fresh screenful. printLines
// records the block as r.last, so the match stays climbable.
func (r *repl) showMatch() {
	ml := r.search.line + 1
	if r.last != nil && ml >= r.last.start && ml <= r.last.start+r.last.count-1 {
		r.printLines(r.last.start, r.last.start+r.last.count-1)
		return
	}
	r.printLines(ml, len(r.b.lines))
}

// searchForward scans the buffer for the next match of re at or after
// (fromLine, fromRune), wrapping past the last line back to the top so every line
// is visited once. It returns the match's 0-based line and rune range [lo,hi)
// within that line, and wrapped — true when the scan ran off the end of the
// buffer and resumed at the top, the signal for a "wrapped to top" notice. The
// regexp works in bytes; fromRune and the returned range are runes, converted at
// the edges so the rest of nved's rune-based cursor math lines up.
func searchForward(lines []string, re *regexp.Regexp, fromLine, fromRune int) (line, lo, hi int, wrapped, ok bool) {
	n := len(lines)
	for d := 0; d < n; d++ {
		li := (fromLine + d) % n
		text := lines[li]
		startByte := 0
		if d == 0 && fromRune > 0 {
			startByte = runeToByte(text, fromRune)
		}
		loc := re.FindStringIndex(text[startByte:])
		if loc == nil {
			continue
		}
		bLo, bHi := startByte+loc[0], startByte+loc[1]
		return li, utf8.RuneCountInString(text[:bLo]), utf8.RuneCountInString(text[:bHi]), fromLine+d >= n, true
	}
	return 0, 0, 0, false, false
}

// runeToByte returns the byte offset of rune index r in s, clamped to len(s) when
// r runs past the end — the inverse of the utf8.RuneCountInString the search uses
// to report rune positions.
func runeToByte(s string, r int) int {
	if r <= 0 {
		return 0
	}
	i := 0
	for bi := range s {
		if i == r {
			return bi
		}
		i++
	}
	return len(s)
}
