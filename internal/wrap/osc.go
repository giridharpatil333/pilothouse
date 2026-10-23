package wrap

import (
	"strings"

	"github.com/xasdta/pilothouse/internal/api"
)

// OSC 7501 is Pilothouse's status escape (layer L1): an agent, or a wrapper
// script around one, prints
//
//	ESC ] 7501 ; state=<s> [; app=<a>] [; kind=<k>] (BEL | ESC \)
//
// and the holder turns it into evidence for the daemon. Terminals ignore
// OSC numbers they don't know, so the sequence is passed through to the
// attached terminal unchanged.

const (
	oscNumber = "7501"
	// maxOSCPayload bounds what is buffered for one 7501 sequence; real
	// ones are a few dozen bytes.
	maxOSCPayload = 512
	// maxOSCSkip bounds how long the scanner follows an unterminated OSC
	// of any number. Past it the scanner resynchronises at the next ESC,
	// so a stray "ESC ]" in binary output can't blind it forever.
	maxOSCSkip = 64 << 10
)

type oscState uint8

const (
	oscGround  oscState = iota
	oscEsc              // saw ESC
	oscBody             // inside ESC ] … collecting
	oscBodyEsc          // inside the body, saw ESC (maybe ST)
)

// OSCScanner finds OSC 7501 sequences in a terminal byte stream. It keeps
// state between Feed calls, so sequences split across PTY reads are found.
// Memory is bounded by maxOSCPayload regardless of input. Not safe for
// concurrent use.
type OSCScanner struct {
	st      oscState
	buf     []byte // payload of the current OSC, while it may still be 7501
	skipped int    // bytes seen in the current OSC body
	ignore  bool   // current OSC is not 7501, or is oversized/garbled
}

// Feed scans p and calls emit for every complete, valid 7501 sequence.
func (s *OSCScanner) Feed(p []byte, emit func(api.OSCBody)) {
	for _, c := range p {
		switch s.st {
		case oscGround:
			if c == 0x1b {
				s.st = oscEsc
			}
		case oscEsc:
			switch c {
			case ']':
				s.st = oscBody
				s.buf = s.buf[:0]
				s.skipped = 0
				s.ignore = false
			case 0x1b:
				// ESC ESC: the second one may start a sequence.
			default:
				s.st = oscGround
			}
		case oscBody:
			switch c {
			case 0x07: // BEL terminator (xterm)
				s.finish(emit)
			case 0x1b:
				s.st = oscBodyEsc
			case 0x18, 0x1a: // CAN, SUB abort a control string (ECMA-48)
				s.st = oscGround
			default:
				s.collect(c)
			}
		case oscBodyEsc:
			if c == '\\' { // ESC \ is ST, the standard terminator
				s.finish(emit)
				continue
			}
			// Any other ESC x aborts the OSC; the ESC may begin a new one.
			s.st = oscEsc
			if c == ']' {
				s.st = oscBody
				s.buf = s.buf[:0]
				s.skipped = 0
				s.ignore = false
			} else if c != 0x1b {
				s.st = oscGround
			}
		}
	}
}

func (s *OSCScanner) collect(c byte) {
	s.skipped++
	if s.skipped > maxOSCSkip {
		s.st = oscGround
		return
	}
	if s.ignore {
		return
	}
	// Only printable ASCII is meaningful in a 7501 payload. Anything else
	// means this is not one of ours (or is corrupted): stop buffering.
	if c < 0x20 || c > 0x7e || len(s.buf) >= maxOSCPayload {
		s.ignore = true
		return
	}
	s.buf = append(s.buf, c)
	// Decide early whether this is 7501 so other OSCs (titles, hyperlinks,
	// clipboard) are skipped without buffering.
	if n := len(s.buf); n <= len(oscNumber) {
		if c != oscNumber[n-1] {
			s.ignore = true
		}
	} else if n == len(oscNumber)+1 && c != ';' {
		s.ignore = true
	}
}

func (s *OSCScanner) finish(emit func(api.OSCBody)) {
	s.st = oscGround
	if s.ignore || len(s.buf) <= len(oscNumber)+1 {
		return
	}
	if b, ok := ParseOSC7501(string(s.buf[len(oscNumber)+1:])); ok {
		emit(b)
	}
}

var (
	validOSCStates = map[string]bool{"idle": true, "working": true, "blocked": true, "done": true, "error": true}
	validOSCKinds  = map[string]bool{"permission": true, "question": true, "auth": true}
)

// ParseOSC7501 parses the key=value list after "7501;". state is
// required and must be one of idle|working|blocked|done|error; kind, when
// present, must be permission|question|auth. Unknown keys are ignored so
// agents can add fields without breaking older holders.
func ParseOSC7501(payload string) (api.OSCBody, bool) {
	b := api.OSCBody{Raw: payload}
	for _, field := range strings.Split(payload, ";") {
		k, v, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "state":
			b.State = v
		case "app":
			if len(v) <= 64 {
				b.App = v
			}
		case "kind":
			b.Kind = v
		}
	}
	if !validOSCStates[b.State] {
		return api.OSCBody{}, false
	}
	if b.Kind != "" && !validOSCKinds[b.Kind] {
		b.Kind = ""
	}
	return b, true
}
