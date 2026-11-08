package format

import (
	"cmp"
	"slices"
	"strings"
)

// SortTags sorts tags in place, newest version first: "latest" is pinned to
// the top, runs of digits compare as numbers so "1.10" comes before "1.9",
// and tags that only differ in leading zeros fall back to plain string order.
func SortTags(tags []string) {
	slices.SortFunc(tags, func(a, b string) int {
		switch {
		case a == b:
			return 0
		case a == "latest":
			return -1
		case b == "latest":
			return 1
		}
		if c := natural(b, a); c != 0 {
			return c
		}
		return strings.Compare(b, a)
	})
}

// natural compares a and b chunk by chunk, where a chunk is a run of digits
// or a run of anything else. Digit runs compare by value, others as strings.
func natural(a, b string) int {
	for a != "" && b != "" {
		var x, y string
		x, a = chunk(a)
		y, b = chunk(b)
		var c int
		if isDigit(x[0]) && isDigit(y[0]) {
			c = number(x, y)
		} else {
			c = strings.Compare(x, y)
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a), len(b))
}

// chunk splits s after its leading run of digits or non-digits.
func chunk(s string) (head, rest string) {
	d := isDigit(s[0])
	i := 1
	for i < len(s) && isDigit(s[i]) == d {
		i++
	}
	return s[:i], s[i:]
}

// number compares two digit runs by value without parsing, so any length works.
func number(x, y string) int {
	x = strings.TrimLeft(x, "0")
	y = strings.TrimLeft(y, "0")
	if c := cmp.Compare(len(x), len(y)); c != 0 {
		return c
	}
	return strings.Compare(x, y)
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }
