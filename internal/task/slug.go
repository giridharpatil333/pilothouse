package task

import "strings"

const (
	maxSlugLen   = 40
	maxSlugWords = 6
)

// Slugify turns a prompt or name into a branch-safe slug: lowercase ASCII
// letters and digits joined by single dashes, at most six words and 40
// characters, never empty.
func Slugify(s string) string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			cur.WriteRune(r)
		case r == '\'' || r == '’':
			// "don't" → "dont", not "don-t"
		default:
			flush()
		}
	}
	flush()
	var b strings.Builder
	for i, w := range words {
		if i == maxSlugWords {
			break
		}
		if b.Len() > 0 && b.Len()+1+len(w) > maxSlugLen {
			break
		}
		if b.Len() > 0 {
			b.WriteByte('-')
		}
		b.WriteString(w)
	}
	out := b.String()
	if len(out) > maxSlugLen { // a single very long word
		out = out[:maxSlugLen]
	}
	if out == "" {
		return "task"
	}
	return out
}
