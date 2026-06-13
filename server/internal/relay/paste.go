package relay

import "strings"

// Bracketed-paste control sequences. Wrapping injected text in these makes
// a REPL treat embedded newlines as pasted content (a single multi-line
// block) instead of submitting line-by-line; a separate Enter then submits
// the whole block.
const (
	bracketStart = "\x1b[200~"
	bracketEnd   = "\x1b[201~"
)

// sanitizeForPaste strips any literal bracketed-paste END marker from the
// text so a crafted prompt/summary can't close the paste early and have
// the trailing bytes interpreted as keystrokes.
func sanitizeForPaste(s string) string {
	return strings.ReplaceAll(s, bracketEnd, "")
}

// wrapBracketed sanitizes then wraps text in bracketed-paste markers.
func wrapBracketed(s string) string {
	return bracketStart + sanitizeForPaste(s) + bracketEnd
}

// collapseLines folds a multi-line prompt onto one line for the
// single-line fallback (terminals that don't honour bracketed paste).
// Runs of whitespace become a single space so the prompt submits as one
// REPL line on a single Enter.
func collapseLines(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
