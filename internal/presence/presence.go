// Package presence measures how long the person has been idle at this
// machine. STUB: workstream F replaces the bodies.
package presence

import "time"

// Detector reports OS input idle time. ok=false means unknown on this system.
type Detector interface {
	Idle() (d time.Duration, ok bool)
	Source() string // "macos_hid", "linux_xprintidle", "linux_gnome", "none"
}

type none struct{}

func (none) Idle() (time.Duration, bool) { return 0, false }
func (none) Source() string              { return "none" }

// NewDetector picks the best detector for this OS.
func NewDetector() Detector { return none{} }
