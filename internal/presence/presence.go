// Package presence measures how long the person has been idle at this
// machine. The daemon compares the idle time with
// presence.idle_threshold to decide whether a permission request goes to
// the terminal at the desk or to paired devices.
//
// Every detector shells out to a tool the OS already ships (ioreg on
// macOS, gdbus or xprintidle on Linux) instead of linking IOKit or X11:
// that keeps the binary static and cgo-free. Calls are short-timeout and
// cached because the daemon polls presence often.
package presence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Detector reports OS input idle time. ok=false means unknown on this system.
type Detector interface {
	Idle() (d time.Duration, ok bool)
	Source() string // "macos_hid", "linux_xprintidle", "linux_gnome", "none"
}

// Source names reported by Detector.Source.
const (
	SourceMacHID     = "macos_hid"
	SourceGnome      = "linux_gnome"
	SourceXprintidle = "linux_xprintidle"
	SourceNone       = "none"
)

// Runner runs an external command and returns its stdout. It is a
// variable dependency so tests never depend on the host's tools.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// LookPath reports whether a program exists; injectable for tests.
type LookPath func(name string) (string, error)

// ExecRunner runs a real command.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

const (
	// cmdTimeout bounds one probe: ioreg normally answers in ~10 ms, and a
	// wedged tool must never stall the daemon's routing loop.
	cmdTimeout = 1500 * time.Millisecond
	// cacheFor is the minimum interval between probes. Idle time only
	// matters at a granularity of seconds (threshold defaults to 60 s).
	cacheFor = time.Second
)

type none struct{}

func (none) Idle() (time.Duration, bool) { return 0, false }
func (none) Source() string              { return SourceNone }

// None is the detector for systems where idle time cannot be measured
// (SSH-only boxes, containers). Presence then relies on the manual mode.
func None() Detector { return none{} }

// NewDetector picks the best detector for this OS.
func NewDetector() Detector {
	return newDetector(runtime.GOOS, os.Getenv, exec.LookPath, ExecRunner)
}

func newDetector(goos string, getenv func(string) string, look LookPath, run Runner) Detector {
	switch goos {
	case "darwin":
		return NewCommandDetector(SourceMacHID, run, ParseIOReg, "ioreg", "-c", "IOHIDSystem", "-d", "4")
	case "linux":
		// GNOME's Mutter exposes idle time on the session bus, and works
		// under Wayland where X11 tools see nothing. Try it first, but only
		// keep it if a probe actually answers: gdbus is often installed on
		// non-GNOME desktops too.
		if getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
			if _, err := look("gdbus"); err == nil {
				d := NewCommandDetector(SourceGnome, run, ParseGnomeIdle, "gdbus", gnomeArgs...)
				if _, ok := d.Idle(); ok {
					return d
				}
			}
		}
		if getenv("DISPLAY") != "" {
			if _, err := look("xprintidle"); err == nil {
				d := NewCommandDetector(SourceXprintidle, run, ParseXprintidle, "xprintidle")
				if _, ok := d.Idle(); ok {
					return d
				}
			}
		}
	}
	return none{}
}

var gnomeArgs = []string{
	"call", "--session",
	"--dest", "org.gnome.Mutter.IdleMonitor",
	"--object-path", "/org/gnome/Mutter/IdleMonitor/Core",
	"--method", "org.gnome.Mutter.IdleMonitor.GetIdletime",
}

// CommandDetector runs a command and parses its output into an idle time,
// caching the result for a second.
type CommandDetector struct {
	source string
	run    Runner
	parse  func([]byte) (time.Duration, error)
	name   string
	args   []string
	now    func() time.Time

	mu     sync.Mutex
	at     time.Time
	idle   time.Duration
	ok     bool
	err    error
	probed bool
}

// NewCommandDetector returns a detector that runs name args… and parses
// its stdout with parse.
func NewCommandDetector(source string, run Runner, parse func([]byte) (time.Duration, error), name string, args ...string) *CommandDetector {
	return &CommandDetector{source: source, run: run, parse: parse, name: name, args: args, now: time.Now}
}

// Source implements Detector.
func (d *CommandDetector) Source() string { return d.source }

// Idle implements Detector.
func (d *CommandDetector) Idle() (time.Duration, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if d.probed && now.Sub(d.at) < cacheFor && now.Sub(d.at) >= 0 {
		// The cached value aged by the time since the probe: idle time
		// grows by exactly that much unless input happened, which the
		// next probe will see.
		if d.ok {
			return d.idle + now.Sub(d.at), true
		}
		return 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	out, err := d.run(ctx, d.name, d.args...)
	if err == nil {
		d.idle, err = d.parse(out)
	}
	d.probed, d.at, d.err, d.ok = true, now, err, err == nil
	if !d.ok {
		return 0, false
	}
	return d.idle, true
}

// Err returns the error of the last probe, for `pilothouse doctor`.
func (d *CommandDetector) Err() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

var ioregIdleRE = regexp.MustCompile(`"HIDIdleTime"\s*=\s*(\d+)`)

// ParseIOReg extracts HIDIdleTime (nanoseconds) from `ioreg -c IOHIDSystem`
// output. Some Macs list several IOHIDSystem entries; the smallest value
// is the most recent input on any of them.
func ParseIOReg(out []byte) (time.Duration, error) {
	ms := ioregIdleRE.FindAllSubmatch(out, -1)
	if len(ms) == 0 {
		return 0, errors.New("ioreg: HIDIdleTime not found")
	}
	var best uint64
	for i, m := range ms {
		v, err := strconv.ParseUint(string(m[1]), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("ioreg: bad HIDIdleTime %q", m[1])
		}
		if i == 0 || v < best {
			best = v
		}
	}
	return time.Duration(best), nil
}

var gnomeIdleRE = regexp.MustCompile(`^\(\s*(?:uint64\s+)?(\d+)\s*,?\s*\)$`)

// ParseGnomeIdle parses the gdbus reply of Mutter's GetIdletime, e.g.
// "(uint64 12345,)", in milliseconds.
func ParseGnomeIdle(out []byte) (time.Duration, error) {
	m := gnomeIdleRE.FindSubmatch(bytes.TrimSpace(out))
	if m == nil {
		return 0, fmt.Errorf("gdbus: unexpected reply %q", strings.TrimSpace(string(out)))
	}
	v, err := strconv.ParseUint(string(m[1]), 10, 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(v) * time.Millisecond, nil
}

// ParseXprintidle parses xprintidle's output: idle milliseconds.
func ParseXprintidle(out []byte) (time.Duration, error) {
	s := strings.TrimSpace(string(out))
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("xprintidle: unexpected output %q", s)
	}
	return time.Duration(v) * time.Millisecond, nil
}
