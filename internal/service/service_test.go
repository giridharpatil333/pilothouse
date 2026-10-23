package service

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fakeRunner records commands and answers from a script. Tests never run
// the real launchctl or systemctl.
type fakeRunner struct {
	mu    sync.Mutex
	calls []string
	reply func(cmd string) (string, error)
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	f.calls = append(f.calls, cmd)
	f.mu.Unlock()
	if f.reply == nil {
		return nil, nil
	}
	out, err := f.reply(cmd)
	return []byte(out), err
}

func setupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PILOTHOUSE_USER_HOME", home)
	t.Setenv("PILOTHOUSE_HOME", filepath.Join(home, ".pilothouse"))
	t.Setenv("PILOTHOUSE_RUN_DIR", "/tmp/agtest-run")
	return home
}

// setupFixedHome uses fixed, never-written paths so golden files are
// stable. Only for tests that render without writing.
func setupFixedHome(t *testing.T) {
	t.Helper()
	t.Setenv("PILOTHOUSE_USER_HOME", "/nonexistent/home")
	t.Setenv("PILOTHOUSE_HOME", "/opt/pilothouse-test/home")
}

func newTestManager(goos string, f *fakeRunner) *Manager {
	return &Manager{
		GOOS: goos,
		Exe:  "/usr/local/bin/pilothouse",
		UID:  501,
		Run:  f.run,
		Env:  map[string]string{"PILOTHOUSE_HOME": "/opt/pilothouse-test/home", "PATH": "/opt/homebrew/bin:/usr/bin:/bin"},
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if string(want) != string(got) {
		t.Fatalf("%s mismatch:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestRenderGolden(t *testing.T) {
	setupFixedHome(t)
	for _, goos := range []string{"darwin", "linux"} {
		m := newTestManager(goos, &fakeRunner{})
		b, err := m.Render()
		if err != nil {
			t.Fatal(err)
		}
		name := map[string]string{"darwin": "launchd.plist.golden", "linux": "systemd.service.golden"}[goos]
		golden(t, name, b)
	}
}

func TestRenderRejectsRelativeExe(t *testing.T) {
	setupHome(t)
	m := newTestManager("darwin", &fakeRunner{})
	m.Exe = "pilothouse"
	if _, err := m.Render(); err == nil {
		t.Fatal("want error for relative exe")
	}
	m.GOOS = "plan9"
	m.Exe = "/x"
	if _, err := m.Render(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestQuoting(t *testing.T) {
	tests := map[string]string{
		"/usr/bin/pilothouse":   "/usr/bin/pilothouse",
		"/Users/a b/pilothouse": `"/Users/a b/pilothouse"`,
		"100%":               "100%%",
		`PATH=$HOME/bin`:     `PATH=$$HOME/bin`,
		`say "hi"`:           `"say \"hi\""`,
		"":                   `""`,
	}
	for in, want := range tests {
		if got := systemdQuote(in); got != want {
			t.Errorf("systemdQuote(%q) = %q want %q", in, got, want)
		}
	}
	if got := xmlEscape(`a&b<c>"d'`); got != "a&amp;b&lt;c&gt;&quot;d&apos;" {
		t.Errorf("xmlEscape = %q", got)
	}
}

func TestPathsUseUserHome(t *testing.T) {
	home := setupHome(t)
	if p := newTestManager("darwin", &fakeRunner{}).Path(); p != filepath.Join(home, "Library/LaunchAgents/dev.pilothouse.daemon.plist") {
		t.Fatalf("darwin path %s", p)
	}
	if p := newTestManager("linux", &fakeRunner{}).Path(); p != filepath.Join(home, ".config/systemd/user/pilothouse.service") {
		t.Fatalf("linux path %s", p)
	}
}

func TestLaunchdLifecycle(t *testing.T) {
	setupHome(t)
	loaded := false
	f := &fakeRunner{}
	f.reply = func(cmd string) (string, error) {
		switch {
		case strings.HasPrefix(cmd, "launchctl print"):
			if !loaded {
				return "Could not find service", errors.New("exit status 113")
			}
			return launchctlPrintSample, nil
		case strings.HasPrefix(cmd, "launchctl bootstrap"):
			loaded = true
		case strings.HasPrefix(cmd, "launchctl bootout"):
			loaded = false
		}
		return "", nil
	}
	m := newTestManager("darwin", f)
	ctx := context.Background()

	res, err := m.Install(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("first install must write the plist")
	}
	wantCalls(t, f, "launchctl print gui/501/dev.pilothouse.daemon", "launchctl bootstrap gui/501 "+m.Path())
	if fi, err := os.Stat(m.Path()); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("plist: %v %v", fi, err)
	}

	// Second install: unchanged and loaded → no-op.
	res, err = m.Install(ctx)
	if err != nil || res.Changed {
		t.Fatalf("idempotent install: %+v %v", res, err)
	}
	wantCalls(t, f, "launchctl print gui/501/dev.pilothouse.daemon")

	// Changed binary path → bootout + bootstrap.
	m.Exe = "/opt/homebrew/bin/pilothouse"
	if _, err := m.Install(ctx); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "launchctl print gui/501/dev.pilothouse.daemon", "launchctl bootout gui/501/dev.pilothouse.daemon", "launchctl bootstrap gui/501 "+m.Path())

	st, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Installed || !st.Current || !st.Loaded || !st.Running || st.PID != 4242 {
		t.Fatalf("status = %+v", st)
	}
	f.calls = nil

	if err := m.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "launchctl print gui/501/dev.pilothouse.daemon", "launchctl kickstart -k gui/501/dev.pilothouse.daemon")
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "launchctl print gui/501/dev.pilothouse.daemon", "launchctl bootout gui/501/dev.pilothouse.daemon")
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "launchctl print gui/501/dev.pilothouse.daemon", "launchctl bootstrap gui/501 "+m.Path())
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "launchctl print gui/501/dev.pilothouse.daemon", "launchctl kickstart gui/501/dev.pilothouse.daemon")

	res, err = m.Uninstall(ctx)
	if err != nil || !res.Changed {
		t.Fatalf("uninstall: %+v %v", res, err)
	}
	if _, err := os.Stat(m.Path()); !os.IsNotExist(err) {
		t.Fatal("plist must be removed")
	}
	res, err = m.Uninstall(ctx)
	if err != nil || res.Changed {
		t.Fatalf("second uninstall: %+v %v", res, err)
	}
	st, _ = m.Status(ctx)
	if st.Installed || st.Loaded {
		t.Fatalf("status after uninstall = %+v", st)
	}
	f.calls = nil
	if err := m.Start(ctx); err == nil || !strings.Contains(err.Error(), "service install") {
		t.Fatalf("start when not installed: %v", err)
	}
}

func TestSystemdLifecycle(t *testing.T) {
	setupHome(t)
	t.Setenv("USER", "dev")
	f := &fakeRunner{reply: func(cmd string) (string, error) {
		switch {
		case strings.HasPrefix(cmd, "systemctl --user show"):
			return "LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=777\nUnitFileState=enabled\n", nil
		case strings.HasPrefix(cmd, "loginctl"):
			return "Linger=no\n", nil
		}
		return "", nil
	}}
	m := newTestManager("linux", f)
	ctx := context.Background()
	res, err := m.Install(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "systemctl --user daemon-reload", "systemctl --user enable pilothouse.service",
		"systemctl --user restart pilothouse.service", "loginctl show-user dev --property=Linger")
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "enable-linger") {
		t.Fatalf("notes = %v", res.Notes)
	}
	if _, err := m.Install(ctx); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "systemctl --user daemon-reload", "systemctl --user enable --now pilothouse.service", "loginctl show-user dev --property=Linger")

	st, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Installed || !st.Current || !st.Running || st.PID != 777 || st.Linger == nil || *st.Linger {
		t.Fatalf("status = %+v", st)
	}
	if st.Detail != "active (running), enabled" {
		t.Fatalf("detail = %q", st.Detail)
	}
	f.calls = nil
	for _, op := range []func(context.Context) error{m.Start, m.Stop, m.Restart} {
		if err := op(ctx); err != nil {
			t.Fatal(err)
		}
	}
	wantCalls(t, f, "systemctl --user start pilothouse.service", "systemctl --user stop pilothouse.service", "systemctl --user restart pilothouse.service")

	if _, err := m.Uninstall(ctx); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "systemctl --user disable --now pilothouse.service", "systemctl --user daemon-reload")
}

func TestCommandErrorsAreReported(t *testing.T) {
	setupHome(t)
	f := &fakeRunner{reply: func(cmd string) (string, error) {
		if strings.HasPrefix(cmd, "launchctl bootstrap") {
			return "Bootstrap failed: 5: Input/output error", errors.New("exit status 5")
		}
		return "", errors.New("exit status 113")
	}}
	_, err := newTestManager("darwin", f).Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Input/output error") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnsupported(t *testing.T) {
	setupHome(t)
	m := newTestManager("windows", &fakeRunner{})
	ctx := context.Background()
	if _, err := m.Install(ctx); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err := m.Status(ctx); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	if err := m.Start(ctx); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestParseLaunchctlPrint(t *testing.T) {
	running, pid, detail := ParseLaunchctlPrint(launchctlPrintSample)
	if !running || pid != 4242 || detail != "running, last exit: (never exited)" {
		t.Fatalf("%v %d %q", running, pid, detail)
	}
	running, pid, _ = ParseLaunchctlPrint("gui/501/x = {\n\tstate = not running\n\tlast exit code = 1\n}")
	if running || pid != 0 {
		t.Fatal("not running expected")
	}
}

func TestNewUsesEnvironment(t *testing.T) {
	setupHome(t)
	m := New("/usr/local/bin/pilothouse")
	if m.Env["PILOTHOUSE_HOME"] != os.Getenv("PILOTHOUSE_HOME") || m.Env["PILOTHOUSE_RUN_DIR"] != "/tmp/agtest-run" {
		t.Fatalf("env = %v", m.Env)
	}
}

func wantCalls(t *testing.T, f *fakeRunner, want ...string) {
	t.Helper()
	f.mu.Lock()
	got := f.calls
	f.calls = nil
	f.mu.Unlock()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// launchctlPrintSample is the shape of `launchctl print gui/<uid>/<label>`.
const launchctlPrintSample = `gui/501/dev.pilothouse.daemon = {
	active count = 1
	path = /Users/dev/Library/LaunchAgents/dev.pilothouse.daemon.plist
	type = LaunchAgent
	state = running

	program = /usr/local/bin/pilothouse
	arguments = {
		/usr/local/bin/pilothouse
		daemon
	}

	default environment = {
		PATH => /usr/bin:/bin:/usr/sbin:/sbin
	}

	domain = gui/501 [100005]
	minimum runtime = 10
	exit timeout = 5
	runs = 1
	pid = 4242
	immediate reason = speculative
	forks = 0
	execs = 1
	initialized = 1
	trampolined = 1
	started suspended = 0
	proxy started suspended = 0
	last exit code = (never exited)

	endpoints = {
	}
}
`
