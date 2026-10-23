package codex

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A deliberately small TOML reader for ~/.codex/config.toml. It does not
// build a document model; it records where each table header and each
// key/value starts, which is enough to add, rename and delete
// [hooks.state."<key>"] tables by editing lines, so every comment and
// every byte we do not own survives. Anything it cannot parse is an
// error, and the caller then refuses to edit (Codex would refuse to load
// such a file too).

type tomlTable struct {
	path  []string
	array bool // [[array.of.tables]]
	line  int  // header line (0-based)
}

type tomlKV struct {
	path     []string // full path: enclosing table + dotted key
	raw      string   // value as written
	str      *string  // decoded value when it is a single-line string
	compound bool     // array or inline table
	line     int
	table    int // index into tables; -1 for the root table
}

type tomlDoc struct {
	src    string
	starts []int // byte offset of each line
	tables []tomlTable
	kvs    []tomlKV
}

func parseTOML(src string) (*tomlDoc, error) {
	d := &tomlDoc{src: src, starts: []int{0}}
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			d.starts = append(d.starts, i+1)
		}
	}
	sc := &tomlScanner{s: src}
	var cur []string
	table := -1
	for {
		sc.skipBlank()
		if sc.eof() {
			return d, nil
		}
		at := sc.i
		switch c := sc.s[sc.i]; {
		case c == '\n' || c == '\r':
			if !sc.newline() {
				return nil, d.errAt(at, "stray carriage return")
			}
			continue
		case c == '#':
			sc.skipComment()
		case c == '[':
			array := strings.HasPrefix(sc.s[sc.i:], "[[")
			sc.i++
			if array {
				sc.i++
			}
			sc.skipBlank()
			path, err := sc.key()
			if err != nil {
				return nil, d.errAt(at, err.Error())
			}
			sc.skipBlank()
			closing := "]"
			if array {
				closing = "]]"
			}
			if !strings.HasPrefix(sc.s[sc.i:], closing) {
				return nil, d.errAt(at, "unterminated table header")
			}
			sc.i += len(closing)
			cur = path
			d.tables = append(d.tables, tomlTable{path: path, array: array, line: d.lineOf(at)})
			table = len(d.tables) - 1
		default:
			key, err := sc.key()
			if err != nil {
				return nil, d.errAt(at, err.Error())
			}
			sc.skipBlank()
			if sc.eof() || sc.s[sc.i] != '=' {
				return nil, d.errAt(at, "expected '=' after key")
			}
			sc.i++
			sc.skipBlank()
			raw, str, compound, err := sc.value()
			if err != nil {
				return nil, d.errAt(at, err.Error())
			}
			full := append(append([]string{}, cur...), key...)
			d.kvs = append(d.kvs, tomlKV{path: full, raw: raw, str: str, compound: compound, line: d.lineOf(at), table: table})
		}
		sc.skipBlank()
		if sc.eof() {
			return d, nil
		}
		if sc.s[sc.i] == '#' {
			sc.skipComment()
		}
		if !sc.eof() && !sc.newline() {
			return nil, d.errAt(sc.i, "unexpected text after value")
		}
	}
}

func (d *tomlDoc) lineOf(off int) int {
	return sort.Search(len(d.starts), func(i int) bool { return d.starts[i] > off }) - 1
}

func (d *tomlDoc) errAt(off int, msg string) error {
	return fmt.Errorf("line %d: %s", d.lineOf(off)+1, msg)
}

// lines splits the source; the last element is "" when src ends in "\n".
func (d *tomlDoc) lines() []string { return strings.Split(d.src, "\n") }

// tableEnd is the line where the region of table t ends: the next header,
// or the end of the file.
func (d *tomlDoc) tableEnd(t int) int {
	if t+1 < len(d.tables) {
		return d.tables[t+1].line
	}
	return len(d.starts)
}

// kvsIn returns the key/values whose path starts with prefix.
func (d *tomlDoc) kvsUnder(prefix ...string) []tomlKV {
	var out []tomlKV
	for _, kv := range d.kvs {
		if hasPrefix(kv.path, prefix) {
			out = append(out, kv)
		}
	}
	return out
}

// stringAt returns the string value at an exact path, if one is set.
func (d *tomlDoc) stringAt(path ...string) (string, bool) {
	for _, kv := range d.kvs {
		if equalPath(kv.path, path) && kv.str != nil {
			return *kv.str, true
		}
	}
	return "", false
}

// rawAt returns the raw value at an exact path.
func (d *tomlDoc) rawAt(path ...string) (string, bool) {
	for _, kv := range d.kvs {
		if equalPath(kv.path, path) {
			return kv.raw, true
		}
	}
	return "", false
}

func hasPrefix(path, prefix []string) bool {
	return len(path) >= len(prefix) && equalPath(path[:len(prefix)], prefix)
}

func equalPath(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// quoteKey renders one key segment: bare when possible, else a basic
// string.
func quoteKey(k string) string {
	bare := k != ""
	for _, r := range k {
		if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			bare = false
			break
		}
	}
	if bare {
		return k
	}
	return quoteString(k)
}

func quoteString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func headerFor(path []string) string {
	parts := make([]string, len(path))
	for i, p := range path {
		parts[i] = quoteKey(p)
	}
	return "[" + strings.Join(parts, ".") + "]"
}

type tomlScanner struct {
	s string
	i int
}

func (sc *tomlScanner) eof() bool { return sc.i >= len(sc.s) }

func (sc *tomlScanner) skipBlank() {
	for !sc.eof() && (sc.s[sc.i] == ' ' || sc.s[sc.i] == '\t') {
		sc.i++
	}
}

func (sc *tomlScanner) skipComment() {
	for !sc.eof() && sc.s[sc.i] != '\n' {
		sc.i++
	}
}

func (sc *tomlScanner) newline() bool {
	if strings.HasPrefix(sc.s[sc.i:], "\r\n") {
		sc.i += 2
		return true
	}
	if !sc.eof() && sc.s[sc.i] == '\n' {
		sc.i++
		return true
	}
	return false
}

// skipSpaceLines skips whitespace, newlines and comments (inside arrays
// and multi-line inline tables).
func (sc *tomlScanner) skipSpaceLines() {
	for !sc.eof() {
		switch sc.s[sc.i] {
		case ' ', '\t', '\r', '\n':
			sc.i++
		case '#':
			sc.skipComment()
		default:
			return
		}
	}
}

// key reads a dotted key: bare, "basic" or 'literal' segments.
func (sc *tomlScanner) key() ([]string, error) {
	var path []string
	for {
		sc.skipBlank()
		seg, err := sc.simpleKey()
		if err != nil {
			return nil, err
		}
		path = append(path, seg)
		sc.skipBlank()
		if sc.eof() || sc.s[sc.i] != '.' {
			return path, nil
		}
		sc.i++
	}
}

func (sc *tomlScanner) simpleKey() (string, error) {
	if sc.eof() {
		return "", errors.New("expected a key")
	}
	switch sc.s[sc.i] {
	case '"':
		sc.i++
		return sc.basicString()
	case '\'':
		sc.i++
		return sc.literalString()
	}
	start := sc.i
	for !sc.eof() {
		c := sc.s[sc.i]
		if c == '_' || c == '-' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			sc.i++
			continue
		}
		break
	}
	if sc.i == start {
		return "", fmt.Errorf("invalid key character %q", sc.s[sc.i])
	}
	return sc.s[start:sc.i], nil
}

// basicString reads after the opening quote, decoding escapes.
func (sc *tomlScanner) basicString() (string, error) {
	var b strings.Builder
	for !sc.eof() {
		c := sc.s[sc.i]
		switch {
		case c == '"':
			sc.i++
			return b.String(), nil
		case c == '\n':
			return "", errors.New("newline in string")
		case c == '\\':
			r, err := sc.escape()
			if err != nil {
				return "", err
			}
			b.WriteString(r)
		default:
			b.WriteByte(c)
			sc.i++
		}
	}
	return "", errors.New("unterminated string")
}

func (sc *tomlScanner) escape() (string, error) {
	if sc.i+1 >= len(sc.s) {
		return "", errors.New("bad escape")
	}
	c := sc.s[sc.i+1]
	sc.i += 2
	switch c {
	case 'b':
		return "\b", nil
	case 't':
		return "\t", nil
	case 'n':
		return "\n", nil
	case 'f':
		return "\f", nil
	case 'r':
		return "\r", nil
	case 'e':
		return "\x1b", nil
	case '"':
		return `"`, nil
	case '\\':
		return `\`, nil
	case 'u', 'U':
		n := 4
		if c == 'U' {
			n = 8
		}
		if sc.i+n > len(sc.s) {
			return "", errors.New("bad unicode escape")
		}
		v, err := strconv.ParseUint(sc.s[sc.i:sc.i+n], 16, 32)
		if err != nil || !utf8.ValidRune(rune(v)) {
			return "", errors.New("bad unicode escape")
		}
		sc.i += n
		return string(rune(v)), nil
	}
	return "", fmt.Errorf(`bad escape \%c`, c)
}

func (sc *tomlScanner) literalString() (string, error) {
	end := strings.IndexAny(sc.s[sc.i:], "'\n")
	if end < 0 || sc.s[sc.i+end] != '\'' {
		return "", errors.New("unterminated literal string")
	}
	s := sc.s[sc.i : sc.i+end]
	sc.i += end + 1
	return s, nil
}

// multiline skips a """ or ”' string; the opening delimiter is consumed.
func (sc *tomlScanner) multiline(delim string) error {
	for !sc.eof() {
		if delim == `"""` && sc.s[sc.i] == '\\' {
			sc.i += 2
			continue
		}
		if strings.HasPrefix(sc.s[sc.i:], delim) {
			// Up to two extra quotes may close the content: """a"""""
			n := 0
			for sc.i+n < len(sc.s) && sc.s[sc.i+n] == delim[0] && n < 5 {
				n++
			}
			sc.i += n
			return nil
		}
		sc.i++
	}
	return errors.New("unterminated multi-line string")
}

// value skips one value and returns its text. str is set for single-line
// strings; compound for arrays and inline tables.
func (sc *tomlScanner) value() (raw string, str *string, compound bool, err error) {
	start := sc.i
	if sc.eof() {
		return "", nil, false, errors.New("missing value")
	}
	switch {
	case strings.HasPrefix(sc.s[sc.i:], `"""`), strings.HasPrefix(sc.s[sc.i:], `'''`):
		delim := sc.s[sc.i : sc.i+3]
		sc.i += 3
		err = sc.multiline(delim)
	case sc.s[sc.i] == '"':
		sc.i++
		var s string
		if s, err = sc.basicString(); err == nil {
			str = &s
		}
	case sc.s[sc.i] == '\'':
		sc.i++
		var s string
		if s, err = sc.literalString(); err == nil {
			str = &s
		}
	case sc.s[sc.i] == '[':
		compound = true
		err = sc.array()
	case sc.s[sc.i] == '{':
		compound = true
		err = sc.inlineTable()
	default:
		for !sc.eof() && !strings.ContainsRune(" \t\r\n,]}#", rune(sc.s[sc.i])) {
			sc.i++
		}
		if sc.i == start {
			err = errors.New("missing value")
		}
	}
	return sc.s[start:sc.i], str, compound, err
}

func (sc *tomlScanner) array() error {
	sc.i++ // [
	for {
		sc.skipSpaceLines()
		if sc.eof() {
			return errors.New("unterminated array")
		}
		if sc.s[sc.i] == ']' {
			sc.i++
			return nil
		}
		if _, _, _, err := sc.value(); err != nil {
			return err
		}
		sc.skipSpaceLines()
		if !sc.eof() && sc.s[sc.i] == ',' {
			sc.i++
		}
	}
}

func (sc *tomlScanner) inlineTable() error {
	sc.i++ // {
	for {
		sc.skipSpaceLines()
		if sc.eof() {
			return errors.New("unterminated inline table")
		}
		if sc.s[sc.i] == '}' {
			sc.i++
			return nil
		}
		if _, err := sc.key(); err != nil {
			return err
		}
		sc.skipBlank()
		if sc.eof() || sc.s[sc.i] != '=' {
			return errors.New("expected '=' in inline table")
		}
		sc.i++
		sc.skipBlank()
		if _, _, _, err := sc.value(); err != nil {
			return err
		}
		sc.skipSpaceLines()
		if !sc.eof() && sc.s[sc.i] == ',' {
			sc.i++
		}
	}
}
