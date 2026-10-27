package extract

import (
	"strings"
	"unicode"
)

// Some PDF producers store Hebrew (and Arabic) in visual order, left to right as drawn, so its
// text comes out backwards: "התיכא" for "אכיתה". It can be told from the text itself: Hebrew's
// final letter forms (ך ם ן ף ץ) end words, so in reversed text they start them. A page where
// they mostly start words is turned back line by line, keeping numbers and Latin text, which
// run left to right, the right way round.

func isFinal(r rune) bool { return r == 'ך' || r == 'ם' || r == 'ן' || r == 'ף' || r == 'ץ' }

func isRTL(r rune) bool { return unicode.Is(unicode.Hebrew, r) || unicode.Is(unicode.Arabic, r) }

// visualOrder reports whether a page's Hebrew reads backwards.
func visualOrder(text string) bool {
	start, end := 0, 0
	for _, w := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) }) {
		rs := []rune(w)
		if len(rs) < 2 {
			continue
		}
		if isFinal(rs[0]) {
			start++
		}
		if isFinal(rs[len(rs)-1]) {
			end++
		}
	}
	return start >= 3 && start > 2*end
}

// logicalOrder turns visual-order RTL text back into reading order.
func logicalOrder(text string) string {
	if !visualOrder(text) {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = reverseLine(line)
	}
	return strings.Join(lines, "\n")
}

// reverseLine reverses a line's characters, then reverses back each run of left-to-right
// characters (digits, Latin), so "17 התיכא" becomes "אכיתה 17" and not "71 אכיתה".
func reverseLine(line string) string {
	rs := []rune(line)
	hasRTL := false
	for _, r := range rs {
		if isRTL(r) {
			hasRTL = true
			break
		}
	}
	if !hasRTL {
		return line
	}
	for i, j := 0, len(rs)-1; i < j; i, j = i+1, j-1 {
		rs[i], rs[j] = rs[j], rs[i]
	}
	ltr := func(r rune) bool { return unicode.IsDigit(r) || (unicode.IsLetter(r) && !isRTL(r)) }
	for i := 0; i < len(rs); {
		if !ltr(rs[i]) {
			i++
			continue
		}
		j := i
		// A run continues through the punctuation inside it: "1.5", "e-mail", "Cu(110)".
		for j+1 < len(rs) && (ltr(rs[j+1]) || (!isRTL(rs[j+1]) && !unicode.IsSpace(rs[j+1]) && j+2 < len(rs) && ltr(rs[j+2]))) {
			j++
		}
		for a, b := i, j; a < b; a, b = a+1, b-1 {
			rs[a], rs[b] = rs[b], rs[a]
		}
		i = j + 1
	}
	// Mirrored brackets read the other way once reversed.
	for i, r := range rs {
		switch r {
		case '(':
			rs[i] = ')'
		case ')':
			rs[i] = '('
		case '[':
			rs[i] = ']'
		case ']':
			rs[i] = '['
		}
	}
	return string(rs)
}
