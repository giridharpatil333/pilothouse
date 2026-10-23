package policy

import (
	"path/filepath"
	"runtime"
	"strings"
)

// expandHome replaces a leading "~", "$HOME" or "${HOME}" with home.
// "~user" forms are left alone: resolving other users' homes from a
// policy file would be surprising.
func expandHome(p, home string) string {
	if home == "" {
		return p
	}
	for _, pre := range []string{"~", "${HOME}", "$HOME"} {
		if p == pre {
			return home
		}
		if strings.HasPrefix(p, pre+"/") {
			return home + p[len(pre):]
		}
	}
	return p
}

// absPath makes a tool path absolute the way the agent would resolve it:
// "~" expanded, relative paths joined to cwd, then cleaned.
func absPath(p, cwd, home string) string {
	if p == "" {
		return ""
	}
	p = expandHome(p, home)
	if !filepath.IsAbs(p) && cwd != "" {
		p = filepath.Join(expandHome(cwd, home), p)
	}
	return filepath.Clean(p)
}

// pathPattern turns a user-written path glob into one matched against
// absolute paths: "~" is expanded, and a relative pattern matches at any
// depth ("*.pem" → "**/*.pem", ".env*" → "**/.env*").
func pathPattern(p, home string) string {
	p = expandHome(p, home)
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "**") {
		return p
	}
	return "**/" + strings.TrimPrefix(p, "./")
}

// caseInsensitiveFS is true where the default filesystem ignores case
// (APFS/HFS+ on macOS), so "~/.PILOTHOUSE" and "~/.pilothouse" are the same
// directory for protected-path checks.
var caseInsensitiveFS = runtime.GOOS == "darwin"

func foldPath(p string) string {
	if caseInsensitiveFS {
		return strings.ToLower(p)
	}
	return p
}

// underAny reports whether p (absolute, cleaned, folded) equals or is
// inside one of roots (same form).
func underAny(p string, roots []string) bool {
	for _, r := range roots {
		if p == r || (strings.HasPrefix(p, r) && (strings.HasSuffix(r, "/") || p[len(r)] == '/')) {
			return true
		}
	}
	return false
}
