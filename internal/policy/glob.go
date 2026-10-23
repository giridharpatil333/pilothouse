package policy

import (
	"errors"
	"fmt"
	"math/bits"
	"strings"
	"unicode/utf8"
)

// globMode selects how wildcards treat '/'.
type globMode uint8

const (
	// globText is for free-form strings (command, tool, agent, kind):
	// '*' and '?' match any character including '/', so "git push*"
	// matches "git push origin feature/x".
	globText globMode = iota
	// globPath is for file paths and branch names: '*' and '?' stop at
	// '/', '**' crosses directories, and "**/" may match nothing, so
	// "src/**/*.go" matches "src/main.go".
	globPath
)

const (
	maxGlobAlternatives = 64   // after {a,b} expansion
	maxGlobLen          = 4096 // bytes; patterns are written by people
)

type elemKind uint8

const (
	eLit   elemKind = iota // one literal rune
	eAny                   // '?'
	eStar                  // '*'  (stops at '/' in path mode)
	eSuper                 // '**' (path mode only; crosses '/')
	// eSplit consumes nothing: it is an epsilon fork to i+1 and i+jump.
	// It makes "**/" optional (so "a/**/b" matches "a/b") and a trailing
	// "/**" optional (so "a/**" matches "a" itself). It must be a separate
	// state: putting the skip on the '**' would let it fire again after
	// the '**' consumed text ("a/**/c" would match "a/bc").
	eSplit
)

type globElem struct {
	kind elemKind
	r    rune
	jump int // eSplit only
}

type globAlt struct {
	elems  []globElem
	prefix string // literal runes every match must start with (fast reject)
	suffix string // literal runes every match must end with (fast reject)
	// Fast paths for the overwhelmingly common shapes.
	exact    bool // no wildcards: s == prefix
	prefixed bool // literal prefix + one trailing text-mode '*'
	// chunks is set for text-mode patterns made only of literals and '*'
	// ("*git*push*"): the literal runs between stars, matched greedily
	// with strings.Index — linear and much faster than the NFA.
	chunks []string
}

// glob is a compiled pattern: the union of its brace alternatives.
type glob struct {
	src  string
	mode globMode
	alts []globAlt
}

// compileGlob compiles pattern. Syntax: '*', '**', '?', '{a,b}' (nested
// allowed; a brace group without a top-level comma is literal, as in
// bash), and '\' escapes the next character. There are no character
// classes: '[' is literal because commands contain it ("[ -f x ]").
func compileGlob(pattern string, mode globMode) (*glob, error) {
	if len(pattern) > maxGlobLen {
		return nil, fmt.Errorf("pattern longer than %d bytes", maxGlobLen)
	}
	if !utf8.ValidString(pattern) {
		return nil, errors.New("pattern is not valid UTF-8")
	}
	expanded, err := expandBraces(pattern)
	if err != nil {
		return nil, err
	}
	g := &glob{src: pattern, mode: mode, alts: make([]globAlt, 0, len(expanded))}
	for _, p := range expanded {
		g.alts = append(g.alts, compileAlt(p, mode))
	}
	return g, nil
}

// match reports whether s matches any alternative.
func (g *glob) match(s string) bool {
	for i := range g.alts {
		a := &g.alts[i]
		switch {
		case a.exact:
			if s == a.prefix {
				return true
			}
		case a.prefixed:
			if strings.HasPrefix(s, a.prefix) {
				return true
			}
		case a.chunks != nil:
			if matchChunks(a.chunks, s) {
				return true
			}
		default:
			if len(s) >= len(a.prefix)+len(a.suffix) && strings.HasPrefix(s, a.prefix) &&
				strings.HasSuffix(s, a.suffix) && matchElems(a.elems, s, g.mode == globPath) {
				return true
			}
		}
	}
	return false
}

// expandBraces turns "a{b,c}d" into ["abd", "acd"]. Escapes are kept so
// compileAlt can honour them.
func expandBraces(p string) ([]string, error) {
	open, close, alts := findBraceGroup(p)
	if open < 0 {
		return []string{p}, nil
	}
	prefix, suffix := p[:open], p[close+1:]
	var out []string
	for _, a := range alts {
		rest, err := expandBraces(a + suffix)
		if err != nil {
			return nil, err
		}
		for _, r := range rest {
			out = append(out, prefix+r)
			if len(out) > maxGlobAlternatives {
				return nil, fmt.Errorf("pattern expands to more than %d alternatives", maxGlobAlternatives)
			}
		}
	}
	return out, nil
}

// findBraceGroup finds the first '{' that has a matching '}' and a
// top-level comma, returning its bounds and the split alternatives.
// Groups without a comma ("{}", "{x}") are literal and skipped.
func findBraceGroup(p string) (open, close int, alts []string) {
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '\\':
			i++ // skip escaped byte
		case '{':
			if j, parts := splitBraceGroup(p, i); parts != nil {
				return i, j, parts
			}
		}
	}
	return -1, -1, nil
}

// splitBraceGroup returns the index of the '}' closing the '{' at open and
// the comma-separated alternatives, or nil parts if the group is unclosed
// or has no top-level comma (and so is literal text).
func splitBraceGroup(p string, open int) (int, []string) {
	depth, start := 0, open+1
	var parts []string
	for j := open; j < len(p); j++ {
		switch p[j] {
		case '\\':
			j++
		case '{':
			depth++
		case ',':
			if depth == 1 {
				parts = append(parts, p[start:j])
				start = j + 1
			}
		case '}':
			depth--
			if depth == 0 {
				if parts == nil {
					return j, nil
				}
				return j, append(parts, p[start:j])
			}
		}
	}
	return -1, nil
}

func compileAlt(p string, mode globMode) globAlt {
	pathMode := mode == globPath
	var el []globElem
	for i := 0; i < len(p); {
		c := p[i]
		switch {
		case c == '\\' && i+1 < len(p):
			r, n := utf8.DecodeRuneInString(p[i+1:])
			el = append(el, globElem{kind: eLit, r: r})
			i += 1 + n
		case c == '*':
			j := i
			for j < len(p) && p[j] == '*' {
				j++
			}
			if pathMode && j-i >= 2 {
				switch {
				case j < len(p) && p[j] == '/' && (i == 0 || p[i-1] == '/'):
					// "**/" at the start or after '/' may match nothing.
					el = append(el, globElem{kind: eSplit, jump: 3},
						globElem{kind: eSuper}, globElem{kind: eLit, r: '/'})
					j++
				case j == len(p) && i > 0 && p[i-1] == '/' && len(el) > 0:
					// A trailing "/**" also matches the directory itself:
					// re-emit the '/' as an optional group with the '**'.
					el[len(el)-1] = globElem{kind: eSplit, jump: 3}
					el = append(el, globElem{kind: eLit, r: '/'}, globElem{kind: eSuper})
				default:
					el = append(el, globElem{kind: eSuper})
				}
			} else {
				el = append(el, globElem{kind: eStar})
			}
			i = j
		case c == '?':
			el = append(el, globElem{kind: eAny})
			i++
		default:
			r, n := utf8.DecodeRuneInString(p[i:])
			el = append(el, globElem{kind: eLit, r: r})
			i += n
		}
	}

	a := globAlt{elems: el}
	var pre strings.Builder
	k := 0
	for ; k < len(el) && el[k].kind == eLit; k++ {
		pre.WriteRune(el[k].r)
	}
	a.prefix = pre.String()
	switch {
	case k == len(el):
		a.exact = true
		return a
	case k == len(el)-1 && el[k].kind == eStar && !pathMode:
		a.prefixed = true
		return a
	}
	// Trailing literal run (k marks where the prefix ended, so the two
	// never overlap).
	// Elements inside an optional "**/" group can't be part of it.
	optional := make([]bool, len(el))
	for i, e := range el {
		if e.kind == eSplit {
			for j := i + 1; j < i+e.jump && j < len(el); j++ {
				optional[j] = true
			}
		}
	}
	m := len(el)
	for m > k && el[m-1].kind == eLit && !optional[m-1] {
		m--
	}
	var suf strings.Builder
	for _, e := range el[m:] {
		suf.WriteRune(e.r)
	}
	a.suffix = suf.String()

	if !pathMode {
		var chunks []string
		var cur strings.Builder
		ok := true
		for _, e := range el {
			switch e.kind {
			case eLit:
				cur.WriteRune(e.r)
			case eStar:
				chunks = append(chunks, cur.String())
				cur.Reset()
			default:
				ok = false
			}
		}
		if ok {
			a.chunks = append(chunks, cur.String())
		}
	}
	return a
}

// matchChunks matches c0*c1*…*cn: the first chunk is a prefix, the last
// a suffix, and the middle ones occur in order in between. Taking the
// leftmost occurrence of each middle chunk is optimal because '*' can
// absorb anything.
func matchChunks(chunks []string, s string) bool {
	first, last := chunks[0], chunks[len(chunks)-1]
	if len(s) < len(first)+len(last) || !strings.HasPrefix(s, first) || !strings.HasSuffix(s, last) {
		return false
	}
	mid := s[len(first) : len(s)-len(last)]
	for _, c := range chunks[1 : len(chunks)-1] {
		j := strings.Index(mid, c)
		if j < 0 {
			return false
		}
		mid = mid[j+len(c):]
	}
	return true
}

// matchElems simulates the pattern as an NFA over runes: state i means
// "elements before i are matched". Linear in len(s)*len(el)/64, with no
// backtracking blow-up and no allocation for patterns under 256 elements.
func matchElems(el []globElem, s string, pathMode bool) bool {
	n := len(el)
	words := n/64 + 1
	var bufA, bufB [4]uint64
	var cur, nxt []uint64
	if words <= len(bufA) {
		cur, nxt = bufA[:words], bufB[:words]
	} else {
		cur, nxt = make([]uint64, words), make([]uint64, words)
	}
	setBit(cur, 0)
	closeStates(cur, el)
	for _, r := range s {
		clear(nxt)
		live := false
		for w, word := range cur {
			for word != 0 {
				b := bits.TrailingZeros64(word)
				word &^= 1 << b
				i := w*64 + b
				if i >= n {
					continue // accept state: nothing left to consume input
				}
				switch e := el[i]; e.kind {
				case eLit:
					if e.r == r {
						setBit(nxt, i+1)
						live = true
					}
				case eAny:
					if !pathMode || r != '/' {
						setBit(nxt, i+1)
						live = true
					}
				case eStar:
					if !pathMode || r != '/' {
						setBit(nxt, i)
						live = true
					}
				case eSuper:
					setBit(nxt, i)
					live = true
				}
			}
		}
		if !live {
			return false
		}
		closeStates(nxt, el)
		cur, nxt = nxt, cur
	}
	return cur[n/64]&(1<<(n%64)) != 0
}

// closeStates adds epsilon transitions: stars may match nothing, and
// splits fork. Edges only point forward, so one ascending pass reaches
// the fixed point.
func closeStates(st []uint64, el []globElem) {
	for i := range el {
		if st[i/64]&(1<<(i%64)) == 0 {
			continue
		}
		switch el[i].kind {
		case eStar, eSuper:
			setBit(st, i+1)
		case eSplit:
			setBit(st, i+1)
			setBit(st, min(i+el[i].jump, len(el)))
		}
	}
}

func setBit(st []uint64, i int) { st[i/64] |= 1 << (i % 64) }

// hasGlobMeta reports whether s contains wildcard syntax, so literal
// values (e.g. kind: shell) can be validated against known names.
func hasGlobMeta(s string) bool { return strings.ContainsAny(s, `*?{\`) }
