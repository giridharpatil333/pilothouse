package policy

import (
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
)

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pat  string
		mode globMode
		s    string
		want bool
	}{
		// text mode: '*' crosses '/'
		{"git push*", globText, "git push origin feature/x", true},
		{"git push*", globText, "git push", true},
		{"git push*", globText, "git pull", false},
		{"*git*push*", globText, "cd x && git -C y push -f", true},
		{"*", globText, "", true},
		{"*", globText, "a/b c", true},
		{"", globText, "", true},
		{"", globText, "a", false},
		{"?", globText, "é", true}, // one rune, not one byte
		{"??", globText, "é", false},
		{"a?c", globText, "a/c", true},
		{"a*", globText, "a", true},
		{"*a*b*c*", globText, "xxaxxbxxcxx", true},
		{"*a*b*c*", globText, "xxcxxbxxa", false},
		{"*.go", globText, "dir/x.go", true},
		{"[ -f x ]", globText, "[ -f x ]", true}, // no character classes
		{"a[bc]d", globText, "abd", false},
		// braces
		{"{Bash,Write}", globText, "Write", true},
		{"{Bash,Write}", globText, "Edit", false},
		{"{Bash,Write}", globText, "BashWrite", false},
		{"a{b,c{d,e}}f", globText, "acef", true},
		{"a{b,c{d,e}}f", globText, "abf", true},
		{"a{b,c{d,e}}f", globText, "acf", false},
		{"{,x}y", globText, "y", true},
		{"{,x}y", globText, "xy", true},
		{"{x}", globText, "{x}", true}, // no comma: literal, as in bash
		{"{x}", globText, "x", false},
		{"{}", globText, "{}", true},
		{"a{b,c", globText, "a{b,c", true}, // unclosed: literal
		{"awk '{print $1}'", globText, "awk '{print $1}'", true},
		{"{a,b}{c,d}", globText, "bd", true},
		// escapes
		{`\*`, globText, "*", true},
		{`\*`, globText, "a", false},
		{`\{a,b\}`, globText, "{a,b}", true},
		{`a\`, globText, `a\`, true},
		{`\?`, globText, "x", false},
		// path mode
		{"/a/*", globPath, "/a/b", true},
		{"/a/*", globPath, "/a/b/c", false},
		{"/a/**", globPath, "/a/b/c", true},
		{"/a/**", globPath, "/a", true}, // trailing /** includes the dir
		{"/a/**", globPath, "/a/", true},
		{"/a/**", globPath, "/ab", false},
		{"/a/**/c", globPath, "/a/c", true},
		{"/a/**/c", globPath, "/a/b/c", true},
		{"/a/**/c", globPath, "/a/b/d/c", true},
		{"/a/**/c", globPath, "/a/bc", false},
		{"**/.env", globPath, "/x/.env", true},
		{"**/.env", globPath, ".env", true},
		{"**/.env", globPath, "/x/.envrc", false},
		{"**/.env.*", globPath, "/x/y/.env.local", true},
		{"*.go", globPath, "dir/x.go", false},
		{"*.go", globPath, "x.go", true},
		{"a?b", globPath, "a/b", false},
		{"a**b", globPath, "a/x/b", true},
		{"release/*", globPath, "release/1.2", true},
		{"release/*", globPath, "release/1/2", false},
		{"/a/***/c", globPath, "/a/x/y/c", true},
		{"**", globPath, "", true},
		{"**", globPath, "/any/thing", true},
		{"/a/{b,c}/**", globPath, "/a/c/d", true},
	}
	for _, tt := range tests {
		g, err := compileGlob(tt.pat, tt.mode)
		if err != nil {
			t.Fatalf("compile %q: %v", tt.pat, err)
		}
		if got := g.match(tt.s); got != tt.want {
			t.Errorf("glob(%q, mode %d).match(%q) = %v, want %v", tt.pat, tt.mode, tt.s, got, tt.want)
		}
	}
}

func TestGlobErrors(t *testing.T) {
	for _, pat := range []string{
		"{a,b}{a,b}{a,b}{a,b}{a,b}{a,b}{a,b}", // 128 alternatives
		strings.Repeat("a", maxGlobLen+1),
		"bad\xffutf8",
	} {
		if _, err := compileGlob(pat, globText); err == nil {
			t.Errorf("compileGlob(%.30q) succeeded, want error", pat)
		}
	}
}

func TestGlobLongPatternUsesHeap(t *testing.T) {
	// > 256 elements exercises the non-stack bitset path.
	pat := strings.Repeat("a*", 200)
	g, err := compileGlob(pat, globText)
	if err != nil {
		t.Fatal(err)
	}
	if !g.match(strings.Repeat("ab", 200)) || g.match(strings.Repeat("b", 400)) {
		t.Fatal("long pattern mismatch")
	}
}

func TestGlobPathologicalIsLinear(t *testing.T) {
	g, _ := compileGlob("*a*a*a*a*a*a*a*a*a*b", globText)
	s := strings.Repeat("a", 20000)
	if g.match(s) {
		t.Fatal("should not match")
	}
}

// refRegexp is an independent translation of the glob rules to a regexp,
// used to cross-check the NFA on random inputs.
func refRegexp(pat string, mode globMode) *regexp.Regexp {
	var b strings.Builder
	b.WriteString(`(?s)^`)
	path := mode == globPath
	for i := 0; i < len(pat); {
		switch c := pat[i]; c {
		case '*':
			j := i
			for j < len(pat) && pat[j] == '*' {
				j++
			}
			switch {
			case !path || j-i == 1:
				if path {
					b.WriteString(`[^/]*`)
				} else {
					b.WriteString(`.*`)
				}
			case j < len(pat) && pat[j] == '/' && (i == 0 || pat[i-1] == '/'):
				b.WriteString(`(?:.*/)?`)
				j++ // consumed the '/'
			default:
				b.WriteString(`.*`)
			}
			i = j
		case '?':
			if path {
				b.WriteString(`[^/]`)
			} else {
				b.WriteString(`.`)
			}
			i++
		case '/':
			if path && strings.HasPrefix(pat[i:], "/**") && strings.Trim(pat[i+1:], "*") == "" {
				b.WriteString(`(?:/.*)?`)
				i = len(pat)
				continue
			}
			b.WriteString(`/`)
			i++
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
			i++
		}
	}
	b.WriteString(`$`)
	return regexp.MustCompile(b.String())
}

func TestGlobMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	patAlpha := []string{"a", "b", "/", "*", "**", "?"}
	strAlpha := []string{"a", "b", "/"}
	for n := 0; n < 20000; n++ {
		var pb, sb strings.Builder
		for k := rng.IntN(7); k > 0; k-- {
			pb.WriteString(patAlpha[rng.IntN(len(patAlpha))])
		}
		for k := rng.IntN(9); k > 0; k-- {
			sb.WriteString(strAlpha[rng.IntN(len(strAlpha))])
		}
		pat, s := pb.String(), sb.String()
		for _, mode := range []globMode{globText, globPath} {
			g, err := compileGlob(pat, mode)
			if err != nil {
				t.Fatal(err)
			}
			want := refRegexp(pat, mode).MatchString(s)
			if got := g.match(s); got != want {
				t.Fatalf("mode %d: glob %q vs %q = %v, reference says %v", mode, pat, s, got, want)
			}
		}
	}
}

func TestPathPattern(t *testing.T) {
	home := "/home/u"
	tests := map[string]string{
		"~/.ssh/**":     "/home/u/.ssh/**",
		"~":             "/home/u",
		"$HOME/x":       "/home/u/x",
		".env":          "**/.env",
		"./src/**":      "**/src/**",
		"/abs/*.go":     "/abs/*.go",
		"**/node_mods":  "**/node_mods",
		"src/**/*.test": "**/src/**/*.test",
	}
	for in, want := range tests {
		if got := pathPattern(in, home); got != want {
			t.Errorf("pathPattern(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHasWord(t *testing.T) {
	tests := []struct {
		s, w string
		want bool
	}{
		{"git push --force", "--force", true},
		{"git push --force-with-lease", "--force", false},
		{"x=1;rm -rf /", "rm", true},
		{"rmdir x", "rm", false},
		{"farm", "rm", false},
		{"git push origin HEAD:main", "main", true},
		{"git push origin main-fix", "main", false},
		{"/bin/rm x", "rm", false}, // '/' is part of a word
		{"echo $(rm x)", "rm", true},
		{"", "x", false},
		{"x", "", false},
		{"a a a", "a", true},
		{"aa a", "a", true},
	}
	for _, tt := range tests {
		if got := hasWord(tt.s, tt.w); got != tt.want {
			t.Errorf("hasWord(%q, %q) = %v, want %v", tt.s, tt.w, got, tt.want)
		}
	}
}

func BenchmarkGlobCommand(b *testing.B) {
	g, _ := compileGlob("*git*push*", globText)
	s := "cd /Users/me/src/app && git push origin feature/new-thing"
	for b.Loop() {
		g.match(s)
	}
}

func BenchmarkGlobPath(b *testing.B) {
	g, _ := compileGlob("/Users/me/**/secrets/*.{pem,key}", globPath)
	s := "/Users/me/src/app/config/secrets/server.pem"
	for b.Loop() {
		g.match(s)
	}
}
