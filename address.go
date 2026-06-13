package main

import (
	"strconv"
	"strings"
)

// parseAddress parses an address expression over a buffer of n lines and
// returns an inclusive 1-based range. Out-of-range numbers clamp to the
// nearest valid line and a reversed range is swapped, so a well-formed address
// never fails; ok is false only when the text is not an address at all.
//
// Forms: "5" (one line), "1.3" (range), "." (all), "$" (the last line), with an
// empty side of the separator defaulting to 1 on the left and $ on the right — so
// "N." is N to the end and ".N" is the start to N. The "$" terminator still parses
// ("1.$"), but "1." and "." are the simpler everyday forms.
//
// The "." is the default separator because it sits under the right hand on the
// numeric keypad where there is no comma; addresses are all digits, so "5.20"
// reads unambiguously as lines 5 through 20. A "," may be typed in its place
// anywhere — the two are interchangeable.
func parseAddress(s string, n int) (start, end int, ok bool) {
	s = strings.TrimSpace(s)
	left, right := s, s
	if i := strings.IndexAny(s, ",."); i >= 0 {
		left, right = s[:i], s[i+1:]
		if strings.TrimSpace(left) == "" {
			left = "1"
		}
		if strings.TrimSpace(right) == "" {
			right = "$"
		}
	}
	a, ok1 := resolveAddr(left, n)
	b, ok2 := resolveAddr(right, n)
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	if a > b {
		a, b = b, a
	}
	return a, b, true
}

// resolveAddr turns a single address ("$" or a line number) into a clamped
// 1-based line index.
func resolveAddr(s string, n int) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "$" {
		return n, true
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return clamp(v, 1, n), true
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
