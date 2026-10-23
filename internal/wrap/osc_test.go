package wrap

import (
	"reflect"
	"strings"
	"testing"

	"github.com/xasdta/askgate/internal/api"
)

func scanAll(chunks ...string) []api.OSCBody {
	var s OSCScanner
	var got []api.OSCBody
	for _, c := range chunks {
		s.Feed([]byte(c), func(b api.OSCBody) { got = append(got, b) })
	}
	return got
}

func TestOSCScanner(t *testing.T) {
	working := api.OSCBody{State: "working", App: "claude", Raw: "state=working;app=claude"}
	tests := []struct {
		name string
		in   []string
		want []api.OSCBody
	}{
		{"BEL terminator", []string{"\x1b]7501;state=working;app=claude\x07"}, []api.OSCBody{working}},
		{"ST terminator", []string{"\x1b]7501;state=working;app=claude\x1b\\"}, []api.OSCBody{working}},
		{"surrounded by output", []string{"hello \x1b[1mbold\x1b[0m\x1b]7501;state=working;app=claude\x07 world"}, []api.OSCBody{working}},
		{"split in the introducer", []string{"abc\x1b", "]75", "01;state=working;app=claude\x07"}, []api.OSCBody{working}},
		{"split before ST backslash", []string{"\x1b]7501;state=working;app=claude\x1b", "\\"}, []api.OSCBody{working}},
		{"blocked with kind", []string{"\x1b]7501;state=blocked;kind=permission\x07"},
			[]api.OSCBody{{State: "blocked", Kind: "permission", Raw: "state=blocked;kind=permission"}}},
		{"unknown kind dropped", []string{"\x1b]7501;state=blocked;kind=bogus\x07"},
			[]api.OSCBody{{State: "blocked", Raw: "state=blocked;kind=bogus"}}},
		{"unknown keys ignored", []string{"\x1b]7501;x=1;state=idle;y\x07"},
			[]api.OSCBody{{State: "idle", Raw: "x=1;state=idle;y"}}},
		{"two in a row", []string{"\x1b]7501;state=idle\x07\x1b]7501;state=done\x1b\\"},
			[]api.OSCBody{{State: "idle", Raw: "state=idle"}, {State: "done", Raw: "state=done"}}},
		{"invalid state", []string{"\x1b]7501;state=sleeping\x07"}, nil},
		{"missing state", []string{"\x1b]7501;app=x\x07"}, nil},
		{"empty payload", []string{"\x1b]7501\x07", "\x1b]7501;\x07"}, nil},
		{"other OSC number", []string{"\x1b]0;state=working\x07\x1b]75012;state=idle\x07\x1b]750;state=idle\x07"}, nil},
		{"title OSC then ours", []string{"\x1b]0;my title\x07\x1b]7501;state=error\x07"},
			[]api.OSCBody{{State: "error", Raw: "state=error"}}},
		{"control byte inside payload", []string{"\x1b]7501;state=wo\x01rking\x07"}, nil},
		{"non-ASCII inside payload", []string{"\x1b]7501;state=idle;app=é\x07"}, nil},
		{"CAN aborts", []string{"\x1b]7501;state=idle\x18\x07"}, nil},
		{"ESC inside aborts, new sequence starts", []string{"\x1b]7501;state=idle\x1b]7501;state=done\x07"},
			[]api.OSCBody{{State: "done", Raw: "state=done"}}},
		{"ESC [ inside aborts", []string{"\x1b]7501;state=idle\x1b[0m\x07"}, nil},
		{"unterminated then garbage", []string{"\x1b]7501;state=idle", "more text with no end"}, nil},
		{"ESC ESC ]", []string{"\x1b\x1b]7501;state=idle\x07"}, []api.OSCBody{{State: "idle", Raw: "state=idle"}}},
		{"spaces trimmed", []string{"\x1b]7501; state = working \x07"}, []api.OSCBody{{State: "working", Raw: " state = working "}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scanAll(tt.in...); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// Every way of cutting a stream into two reads finds the same sequences.
func TestOSCScannerEverySplit(t *testing.T) {
	stream := "out\x1b]0;title\x07\x1b]7501;state=blocked;kind=question;app=codex\x1b\\mid\x1b]7501;state=working\x07end"
	want := scanAll(stream)
	if len(want) != 2 {
		t.Fatalf("baseline found %d sequences, want 2", len(want))
	}
	for i := 0; i <= len(stream); i++ {
		if got := scanAll(stream[:i], stream[i:]); !reflect.DeepEqual(got, want) {
			t.Fatalf("split at %d: got %+v, want %+v", i, got, want)
		}
	}
	// Byte at a time.
	chunks := strings.Split(stream, "")
	if got := scanAll(chunks...); !reflect.DeepEqual(got, want) {
		t.Fatalf("byte-wise: got %+v", got)
	}
}

func TestOSCScannerOversize(t *testing.T) {
	big := "\x1b]7501;state=idle;app=" + strings.Repeat("a", maxOSCPayload) + "\x07"
	if got := scanAll(big); got != nil {
		t.Fatalf("oversized sequence accepted: %+v", got)
	}
	// The scanner recovers for the next sequence.
	if got := scanAll(big, "\x1b]7501;state=done\x07"); len(got) != 1 || got[0].State != "done" {
		t.Fatalf("no recovery after oversize: %+v", got)
	}
	// An unterminated OSC of any number is abandoned after maxOSCSkip bytes,
	// so the scanner can't be blinded by a stray ESC ].
	var s OSCScanner
	var got []api.OSCBody
	s.Feed([]byte("\x1b]0;"), func(b api.OSCBody) { got = append(got, b) })
	s.Feed([]byte(strings.Repeat("x", maxOSCSkip+1)), func(b api.OSCBody) { got = append(got, b) })
	s.Feed([]byte("\x1b]7501;state=idle\x07"), func(b api.OSCBody) { got = append(got, b) })
	if len(got) != 1 {
		t.Fatalf("no recovery after unterminated OSC: %+v", got)
	}
	if cap(s.buf) > maxOSCPayload*2 {
		t.Fatalf("scanner buffer grew to %d", cap(s.buf))
	}
}

func FuzzOSCScanner(f *testing.F) {
	f.Add([]byte("\x1b]7501;state=working\x07"))
	f.Add([]byte("\x1b]7501;state=blocked;kind=auth\x1b\\"))
	f.Add([]byte("\x1b\x1b]]7501;;;=\x07\x1b"))
	f.Fuzz(func(t *testing.T, b []byte) {
		var s OSCScanner
		s.Feed(b, func(o api.OSCBody) {
			if !validOSCStates[o.State] {
				t.Fatalf("emitted invalid state %q", o.State)
			}
			if len(o.Raw) > maxOSCPayload {
				t.Fatalf("raw too long: %d", len(o.Raw))
			}
		})
		if len(s.buf) > maxOSCPayload {
			t.Fatalf("buffer exceeded bound: %d", len(s.buf))
		}
	})
}
