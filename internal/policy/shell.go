package policy

import (
	"strconv"
	"strings"
)

// This file is a deliberately small shell reader for the self-protection
// rule. It is not a shell parser: it understands enough of POSIX sh/bash
// (quotes, escapes, ANSI-C strings, operators, redirections, command and
// process substitution) to find what each simple command would run and
// where it would write. It errs towards finding more (an `ask` too many)
// rather than fewer. See docs/POLICY.md "Limits".

// shCmd is one simple command: words with quotes removed, plus its
// redirections.
type shCmd struct {
	words   []string
	redirs  []shRedir
	pipedIn bool // stdin comes from a previous command in a pipeline
}

type shRedir struct {
	op     string // ">", ">>", "<", "<<", "<<<", "&>", ...
	target string // file, fd number, here-doc delimiter or here-string
}

// shParse is a parsed command line.
type shParse struct {
	cmds []shCmd
	subs []string // bodies of $(…), `…`, <(…), >(…): analysed recursively
}

func parseShell(s string) shParse {
	var (
		out     shParse
		cur     shCmd
		word    strings.Builder
		inWord  bool
		quoted  bool // the current word used quotes or escapes
		pending string
	)
	flush := func() {
		if !inWord {
			return
		}
		w := word.String()
		word.Reset()
		inWord = false
		wasQuoted := quoted
		quoted = false
		if pending != "" {
			cur.redirs = append(cur.redirs, shRedir{op: pending, target: w})
			pending = ""
			return
		}
		// Unquoted braces are grouping keywords, not arguments.
		if !wasQuoted && (w == "{" || w == "}") {
			if len(cur.words) > 0 || len(cur.redirs) > 0 {
				out.cmds = append(out.cmds, cur)
			}
			cur = shCmd{}
			return
		}
		cur.words = append(cur.words, w)
	}
	end := func(pipe bool) {
		flush()
		pending = ""
		if len(cur.words) > 0 || len(cur.redirs) > 0 {
			out.cmds = append(out.cmds, cur)
		}
		cur = shCmd{pipedIn: pipe}
	}

	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\':
			if i+1 < len(s) {
				if s[i+1] != '\n' { // backslash-newline is a line continuation
					word.WriteByte(s[i+1])
					inWord, quoted = true, true
				}
				i += 2
			} else {
				word.WriteByte('\\')
				inWord = true
				i++
			}
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				word.WriteString(s[i+1:])
				i = len(s)
			} else {
				word.WriteString(s[i+1 : i+1+j])
				i += j + 2
			}
			inWord, quoted = true, true
		case c == '"':
			i = readDoubleQuoted(s, i+1, &word, &out)
			inWord, quoted = true, true
		case c == '$' && i+1 < len(s) && s[i+1] == '\'':
			i = readANSIC(s, i+2, &word)
			inWord, quoted = true, true
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			e := matchParen(s, i+2)
			out.subs = append(out.subs, s[i+2:e])
			word.WriteString(s[i:min(e+1, len(s))])
			inWord = true
			i = e + 1
		case c == '`':
			e := closeBacktick(s, i+1)
			out.subs = append(out.subs, strings.ReplaceAll(s[i+1:e], "\\`", "`"))
			word.WriteString(s[i:min(e+1, len(s))])
			inWord = true
			i = e + 1
		case (c == '<' || c == '>') && i+1 < len(s) && s[i+1] == '(' && !inWord:
			e := matchParen(s, i+2) // process substitution
			out.subs = append(out.subs, s[i+2:e])
			word.WriteString(s[i:min(e+1, len(s))])
			inWord = true
			i = e + 1
		case c == '<' || c == '>':
			if inWord && !quoted && isDigits(word.String()) {
				word.Reset() // "2>": the digits name a file descriptor
				inWord = false
			} else {
				flush()
			}
			op := readRedirOp(s, i)
			pending = op
			i += len(op)
		case c == '&':
			switch {
			case strings.HasPrefix(s[i:], "&>>"):
				flush()
				pending = "&>>"
				i += 3
			case strings.HasPrefix(s[i:], "&>"):
				flush()
				pending = "&>"
				i += 2
			case strings.HasPrefix(s[i:], "&&"):
				end(false)
				i += 2
			default:
				end(false)
				i++
			}
		case c == '|':
			switch {
			case strings.HasPrefix(s[i:], "||"):
				end(false)
				i += 2
			case strings.HasPrefix(s[i:], "|&"):
				end(true)
				i += 2
			default:
				end(true)
				i++
			}
		case c == ';' || c == '\n' || c == '(' || c == ')':
			end(false)
			i++
		case c == ' ' || c == '\t' || c == '\r':
			flush()
			i++
		case c == '#' && !inWord:
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				i = len(s)
			} else {
				i += j
			}
		default:
			word.WriteByte(c)
			inWord = true
			i++
		}
	}
	end(false)
	return out
}

func readRedirOp(s string, i int) string {
	for _, op := range []string{"<<<", "<<-", ">>", ">|", ">&", "<<", "<>", "<&", ">", "<"} {
		if strings.HasPrefix(s[i:], op) {
			return op
		}
	}
	return s[i : i+1]
}

// readDoubleQuoted consumes a "…" string starting after the opening quote
// and returns the index after the closing quote. Command substitutions
// inside are recorded for recursive analysis.
func readDoubleQuoted(s string, i int, w *strings.Builder, out *shParse) int {
	for i < len(s) {
		c := s[i]
		switch {
		case c == '"':
			return i + 1
		case c == '\\' && i+1 < len(s):
			switch n := s[i+1]; n {
			case '$', '`', '"', '\\':
				w.WriteByte(n)
			case '\n':
			default:
				w.WriteByte('\\')
				w.WriteByte(n)
			}
			i += 2
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			e := matchParen(s, i+2)
			out.subs = append(out.subs, s[i+2:e])
			w.WriteString(s[i:min(e+1, len(s))])
			i = e + 1
		case c == '`':
			e := closeBacktick(s, i+1)
			out.subs = append(out.subs, strings.ReplaceAll(s[i+1:e], "\\`", "`"))
			w.WriteString(s[i:min(e+1, len(s))])
			i = e + 1
		default:
			w.WriteByte(c)
			i++
		}
	}
	return i
}

// readANSIC consumes a $'…' string (bash ANSI-C quoting), decoding the
// escapes an obfuscated command might use: \xHH, \NNN, \n, \t, \\, \'.
func readANSIC(s string, i int, w *strings.Builder) int {
	for i < len(s) {
		c := s[i]
		if c == '\'' {
			return i + 1
		}
		if c != '\\' || i+1 >= len(s) {
			w.WriteByte(c)
			i++
			continue
		}
		n := s[i+1]
		switch {
		case n == 'x':
			j := i + 2
			for j < len(s) && j < i+4 && isHex(s[j]) {
				j++
			}
			if v, err := strconv.ParseUint(s[i+2:j], 16, 8); err == nil {
				w.WriteByte(byte(v))
			}
			i = j
		case n >= '0' && n <= '7':
			j := i + 1
			for j < len(s) && j < i+4 && s[j] >= '0' && s[j] <= '7' {
				j++
			}
			if v, err := strconv.ParseUint(s[i+1:j], 8, 8); err == nil {
				w.WriteByte(byte(v))
			}
			i = j
		case n == 'n':
			w.WriteByte('\n')
			i += 2
		case n == 't':
			w.WriteByte('\t')
			i += 2
		default:
			w.WriteByte(n)
			i += 2
		}
	}
	return i
}

// matchParen returns the index of the ')' closing a group whose body
// starts at i, skipping quoted text; len(s) if unclosed.
func matchParen(s string, i int) int {
	depth := 1
	for i < len(s) {
		switch s[i] {
		case '\\':
			i++
		case '\'':
			if j := strings.IndexByte(s[i+1:], '\''); j >= 0 {
				i += j + 1
			} else {
				return len(s)
			}
		case '"':
			for i++; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' {
					i++
				}
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
		i++
	}
	return len(s)
}

func closeBacktick(s string, i int) int {
	for ; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '`' {
			return i
		}
	}
	return len(s)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// isAssignment reports whether w is a leading NAME=value prefix.
func isAssignment(w string) bool {
	eq := strings.IndexByte(w, '=')
	if eq <= 0 {
		return false
	}
	for i := 0; i < eq; i++ {
		c := w[i]
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (i > 0 && c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// cmdBase is the lower-cased executable name of a command word, so
// "./pilothouse", "/opt/bin/pilothouse" and "PILOTHOUSE" (case-insensitive
// filesystems) all read as "pilothouse".
func cmdBase(w string) string {
	if j := strings.LastIndexByte(w, '/'); j >= 0 {
		w = w[j+1:]
	}
	return strings.ToLower(w)
}
