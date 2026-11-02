package llm

import (
	"strings"
	"unicode"
)

// MaxResponseBytes caps how much of a provider response body is read.
const MaxResponseBytes = 8 << 20

// StripControl drops control characters (including terminal escapes) and
// format characters (bidi overrides such as U+202E, isolates, zero-width
// characters) from text supplied by a remote endpoint, so it can neither
// drive a terminal nor visually reorder a report.
func StripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}
