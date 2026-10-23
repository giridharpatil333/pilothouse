package tui

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// queryTimeout bounds the one terminal query the inbox makes. Terminals
// that answer do so in a few milliseconds; termenv's own query waits up
// to 5 s for ones that never do, which is why the inbox asks by itself.
const queryTimeout = 300 * time.Millisecond

var oscBgRE = regexp.MustCompile(`\x1b\]11;rgb:([0-9a-fA-F]{1,4})/([0-9a-fA-F]{1,4})/([0-9a-fA-F]{1,4})`)

// darkBackground reports whether the terminal background is dark. It
// trusts $COLORFGBG when set; otherwise it asks the terminal for its
// background colour (OSC 11) followed by a device-attributes request
// (DA1), which every terminal answers — so a terminal that ignores
// OSC 11 ends the wait at once instead of after the timeout. Unknown
// means dark: by far the common case for developer terminals.
func darkBackground(in, out *os.File, timeout time.Duration) bool {
	if v := os.Getenv("COLORFGBG"); v != "" {
		parts := strings.Split(v, ";")
		if bg, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			return bg != 7 && bg != 15
		}
	}
	if !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(out.Fd())) {
		return true
	}
	old, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return true
	}
	defer func() { _ = term.Restore(int(in.Fd()), old) }()
	if _, err := out.WriteString("\x1b]11;?\x1b\\\x1b[c"); err != nil {
		return true
	}
	reply := readReply(int(in.Fd()), timeout)
	if dark, ok := parseBackground(reply); ok {
		return dark
	}
	return true
}

// readReply reads from fd until a DA1 response ("ESC [ ? … c") arrives
// or the timeout passes.
func readReply(fd int, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	var buf []byte
	tmp := make([]byte, 256)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return string(buf)
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, int(left.Milliseconds())+1)
		if err == unix.EINTR {
			continue
		}
		if err != nil || n == 0 {
			return string(buf)
		}
		k, err := unix.Read(fd, tmp)
		if err != nil || k <= 0 {
			return string(buf)
		}
		buf = append(buf, tmp[:k]...)
		if i := strings.Index(string(buf), "\x1b[?"); i >= 0 && strings.Contains(string(buf[i:]), "c") {
			return string(buf)
		}
	}
}

// parseBackground reads an OSC 11 reply such as
// "ESC ] 11 ; rgb:1e1e/1e1e/2e2e ESC \" and reports whether it is dark.
func parseBackground(reply string) (dark, ok bool) {
	m := oscBgRE.FindStringSubmatch(reply)
	if m == nil {
		return false, false
	}
	var c [3]float64
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseUint(m[i+1], 16, 32)
		if err != nil {
			return false, false
		}
		max := float64(uint64(1)<<(4*len(m[i+1])) - 1)
		c[i] = float64(v) / max
	}
	// Relative luminance (Rec. 709 weights) is enough to tell dark from light.
	lum := 0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2]
	return lum < 0.5, true
}
