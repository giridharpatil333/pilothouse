package presence

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseIORegFixture(t *testing.T) {
	// Captured once from `ioreg -c IOHIDSystem -d 4` on an Apple-silicon Mac.
	b, err := os.ReadFile("testdata/ioreg_darwin.txt")
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseIOReg(b)
	if err != nil {
		t.Fatal(err)
	}
	if d != 26243791*time.Nanosecond {
		t.Fatalf("idle = %v", d)
	}
}

func TestParseIOReg(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"single", `    | |     "HIDIdleTime" = 5000000000`, 5 * time.Second, false},
		{"smallest of several", "\"HIDIdleTime\" = 9000000000\n\"HIDIdleTime\" = 2000000000\n", 2 * time.Second, false},
		{"missing", `"IOClass" = "IOHIDSystem"`, 0, true},
		{"empty", ``, 0, true},
		{"overflow", `"HIDIdleTime" = 999999999999999999999999`, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIOReg([]byte(tt.in))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestParseGnomeIdle(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"(uint64 12345,)\n", 12345 * time.Millisecond, false},
		{"(uint64 0,)", 0, false},
		{"(12,)", 12 * time.Millisecond, false},
		{"Error: GDBus.Error:org.freedesktop.DBus.Error.ServiceUnknown: The name is not activatable", 0, true},
		{"", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseGnomeIdle([]byte(tt.in))
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ParseGnomeIdle(%q) = %v, %v", tt.in, got, err)
		}
	}
}

func TestParseXprintidle(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"4321\n", 4321 * time.Millisecond, false},
		{"0", 0, false},
		{"couldn't open display", 0, true},
		{"-5", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseXprintidle([]byte(tt.in))
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ParseXprintidle(%q) = %v, %v", tt.in, got, err)
		}
	}
}

func TestCommandDetectorCaches(t *testing.T) {
	var calls atomic.Int32
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls.Add(1)
		if _, ok := ctx.Deadline(); !ok {
			t.Error("probe must run with a timeout")
		}
		return []byte(`"HIDIdleTime" = 3000000000`), nil
	}
	d := NewCommandDetector(SourceMacHID, run, ParseIOReg, "ioreg")
	now := time.Unix(1000, 0)
	d.now = func() time.Time { return now }

	if v, ok := d.Idle(); !ok || v != 3*time.Second {
		t.Fatalf("first = %v %v", v, ok)
	}
	now = now.Add(500 * time.Millisecond)
	if v, ok := d.Idle(); !ok || v != 3500*time.Millisecond {
		t.Fatalf("cached = %v %v", v, ok)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 (cached)", calls.Load())
	}
	now = now.Add(time.Second)
	d.Idle()
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 after cache expiry", calls.Load())
	}
	if d.Source() != SourceMacHID || d.Err() != nil {
		t.Fatalf("source/err = %s %v", d.Source(), d.Err())
	}
}

func TestCommandDetectorFailure(t *testing.T) {
	run := func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("boom") }
	d := NewCommandDetector(SourceXprintidle, run, ParseXprintidle, "xprintidle")
	if _, ok := d.Idle(); ok {
		t.Fatal("want unknown")
	}
	if d.Err() == nil {
		t.Fatal("want error")
	}
	// Failure is cached too, so a missing tool isn't re-run in a hot loop.
	if _, ok := d.Idle(); ok {
		t.Fatal("want unknown")
	}
}

func TestNewDetectorSelection(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	have := func(names ...string) LookPath {
		return func(n string) (string, error) {
			for _, x := range names {
				if x == n {
					return "/usr/bin/" + n, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	reply := func(byName map[string]string) Runner {
		return func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if out, ok := byName[name]; ok {
				return []byte(out), nil
			}
			return nil, errors.New("exit 1")
		}
	}
	tests := []struct {
		name string
		goos string
		env  map[string]string
		look LookPath
		run  Runner
		want string
	}{
		{"mac", "darwin", nil, have(), reply(nil), SourceMacHID},
		{"gnome", "linux", map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:x", "DISPLAY": ":0"}, have("gdbus", "xprintidle"),
			reply(map[string]string{"gdbus": "(uint64 10,)", "xprintidle": "5"}), SourceGnome},
		{"gnome broken falls back to xprintidle", "linux", map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:x", "DISPLAY": ":0"},
			have("gdbus", "xprintidle"), reply(map[string]string{"xprintidle": "5"}), SourceXprintidle},
		{"ssh box", "linux", map[string]string{"SSH_CONNECTION": "1 2 3 4"}, have("gdbus", "xprintidle"),
			reply(map[string]string{"gdbus": "(uint64 10,)", "xprintidle": "5"}), SourceNone},
		{"no tools", "linux", map[string]string{"DISPLAY": ":0"}, have(), reply(nil), SourceNone},
		{"other os", "freebsd", nil, have(), reply(nil), SourceNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newDetector(tt.goos, env(tt.env), tt.look, tt.run)
			if got.Source() != tt.want {
				t.Fatalf("source = %s want %s", got.Source(), tt.want)
			}
		})
	}
	if _, ok := None().Idle(); ok {
		t.Fatal("none must be unknown")
	}
}
