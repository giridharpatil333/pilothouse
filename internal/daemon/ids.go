package daemon

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newID returns prefix + 10 lowercase base32 characters (50 random bits),
// the id format of BUILD-SPEC §4.
func newID(prefix string) string {
	var b [7]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms; if it does, ids
		// would collide and nothing downstream is safe.
		panic("askgate: crypto/rand failed: " + err.Error())
	}
	return prefix + strings.ToLower(idEncoding.EncodeToString(b[:]))[:10]
}

// validSessionID accepts the ids holders choose for wrapped sessions:
// "s_" followed by 1–40 lowercase letters, digits, '-' or '_'.
func validSessionID(id string) bool {
	rest, ok := strings.CutPrefix(id, "s_")
	if !ok || rest == "" || len(rest) > 40 {
		return false
	}
	for _, c := range rest {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
