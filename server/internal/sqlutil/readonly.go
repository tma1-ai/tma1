package sqlutil

import (
	"fmt"
	"strings"
)

// ValidateSelect accepts a single SELECT statement and returns it with
// any trailing semicolon removed, ready to be wrapped by LimitedSelect.
//
// The MCP `exec_query` tool hands an LLM a raw SQL channel into the
// user's local database, so the accepted surface is deliberately one
// statement kind. In GreptimeDB every read goes through SELECT; writes
// and admin operations are separate top-level statements (INSERT,
// ALTER, ADMIN, ...), so "first keyword is SELECT + exactly one
// statement" is sufficient without a keyword denylist.
//
// The returned statement is the caller's original text: comments are
// skipped for inspection but never rewritten away.
func ValidateSelect(sql string) (string, error) {
	stmt := strings.TrimSpace(sql)
	if stmt == "" {
		return "", fmt.Errorf("empty statement")
	}
	if head, rest, found := splitFirstStatement(stmt); found {
		if strings.TrimSpace(rest) != "" {
			return "", fmt.Errorf("multiple statements are not allowed; send one query at a time")
		}
		stmt = strings.TrimSpace(head)
	}
	verb := firstKeyword(stmt)
	if verb == "" {
		return "", fmt.Errorf("empty statement")
	}
	if verb != "SELECT" {
		return "", fmt.Errorf("%s is not allowed; exec_query accepts a single SELECT statement", verb)
	}
	return stmt, nil
}

// LimitedSelect wraps a validated statement so GreptimeDB itself caps
// the result set, instead of streaming everything back for Go to throw
// away. Asking for one row more than needed is how the caller detects
// truncation.
//
// The newlines matter: a statement ending in a `--` comment would
// otherwise swallow the closing parenthesis.
func LimitedSelect(stmt string, rowLimit int) string {
	return fmt.Sprintf("SELECT * FROM (\n%s\n) LIMIT %d", stmt, rowLimit+1)
}

// firstKeyword returns the upper-cased first word of s, skipping
// leading whitespace, `--` line comments and `/* */` block comments.
func firstKeyword(s string) string {
	i := skipLeadingNoise(s, 0)
	start := i
	for i < len(s) && isWordByte(s[i]) {
		i++
	}
	return strings.ToUpper(s[start:i])
}

func skipLeadingNoise(s string, i int) int {
	for i < len(s) {
		switch {
		case s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r':
			i++
		case startsLineComment(s, i):
			i = skipLineComment(s, i)
		case startsBlockComment(s, i):
			i = skipBlockComment(s, i)
		default:
			return i
		}
	}
	return i
}

func startsLineComment(s string, i int) bool {
	return s[i] == '-' && i+1 < len(s) && s[i+1] == '-'
}

func startsBlockComment(s string, i int) bool {
	return s[i] == '/' && i+1 < len(s) && s[i+1] == '*'
}

func skipLineComment(s string, i int) int {
	for i < len(s) && s[i] != '\n' {
		i++
	}
	return i
}

// skipBlockComment returns the index just past the /* */ comment
// starting at i. An unterminated comment consumes the rest of the
// input; GreptimeDB rejects it on its own.
func skipBlockComment(s string, i int) int {
	for i += 2; i+1 < len(s); i++ {
		if s[i] == '*' && s[i+1] == '/' {
			return i + 2
		}
	}
	return len(s)
}

// splitFirstStatement cuts s at the first semicolon outside a string
// literal or comment.
func splitFirstStatement(s string) (head, rest string, found bool) {
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\'':
			i = endOfLiteral(s, i)
		case startsLineComment(s, i):
			i = skipLineComment(s, i)
		case startsBlockComment(s, i):
			i = skipBlockComment(s, i)
		case s[i] == ';':
			return s[:i], s[i+1:], true
		default:
			i++
		}
	}
	return s, "", false
}

// endOfLiteral returns the index just past the single-quoted literal
// starting at i. A doubled quote escapes a literal quote. An
// unterminated literal consumes the rest of the input so the caller's
// loop stays finite; GreptimeDB rejects it on its own.
func endOfLiteral(s string, i int) int {
	i++
	for i < len(s) {
		if s[i] != '\'' {
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] == '\'' {
			i += 2
			continue
		}
		return i + 1
	}
	return len(s)
}

func isWordByte(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}
