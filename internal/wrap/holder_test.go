package wrap

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xasdta/pilothouse/internal/api"
	"github.com/xasdta/pilothouse/internal/paths"
)

type holderRun struct {
	sid  string
	sock string
	code int
	err  error
	done chan struct{}
	stop context.CancelFunc
}

// startHolder runs a holder in-process (fast, race-checked) for argv.
func startHolder(t *testing.T, cfg HolderConfig) *holderRun {
	t.Helper()
	if cfg.SessionID == "" {
		cfg.SessionID = NewSessionID()
	}
	if cfg.Linger == 0 {
		cfg.Linger = 300 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &holderRun{sid: cfg.SessionID, sock: paths.HolderSocket(cfg.SessionID), done: make(chan struct{}), stop: cancel}
	go func() {
		defer close(h.done)
		h.code, h.err = RunHolder(ctx, cfg)
	}()
	waitFor(t, 5*time.Second, "holder socket", func() bool {
		_, err := Probe(context.Background(), h.sock)
		return err == nil
	})
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(15 * time.Second):
			t.Error("holder did not stop")
		}
	})
	return h
}

func (h *holderRun) wait(t *testing.T) int {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(20 * time.Second):
		t.Fatal("holder did not finish")
	}
	if h.err != nil {
		t.Fatalf("holder: %v", h.err)
	}
	return h.code
}

func TestHolderWithoutDaemon(t *testing.T) {
	testEnv(t)
	h := startHolder(t, HolderConfig{Argv: []string{"/bin/sh", "-c", fakeAgent}, Name: "fake"})

	info, err := Probe(context.Background(), h.sock)
	if err != nil {
		t.Fatal(err)
	}
	if info.SessionID != h.sid || info.Name != "fake" || info.Agent != "generic" || info.PID == 0 || info.Exited {
		t.Fatalf("probe: %+v", info)
	}
	fi, err := os.Stat(h.sock)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode: %v %v", fi.Mode(), err)
	}

	s := attachSession(t, h.sock, true)
	s.waitOutput("ready")
	s.send("ping\r")
	s.waitOutput("got:ping")
	f := s.waitFrame(FrameExit)
	if code, _ := DecodeExit(f.Payload); code != 3 {
		t.Fatalf("exit frame code %d, want 3", code)
	}
	if code := h.wait(t); code != 3 {
		t.Fatalf("holder returned %d, want 3", code)
	}
	if _, err := os.Stat(h.sock); !os.IsNotExist(err) {
		t.Fatalf("socket not removed: %v", err)
	}
	// The OSC sequence passes through to the terminal untouched.
	if !strings.Contains(s.output(), "\x1b]7501;state=working;app=fake\x07") {
		t.Fatalf("OSC not passed through: %q", s.output())
	}
}

func TestHolderEnvironment(t *testing.T) {
	testEnv(t)
	h := startHolder(t, HolderConfig{
		Argv:   []string{"/bin/sh", "-c", `echo "env:$PILOTHOUSE:$PILOTHOUSE_SESSION_ID:$PILOTHOUSE_TASK_ID:$TERM"; stty size; read x`},
		TaskID: "t_abc",
		// No TERM: the holder supplies one. Stale ids from an outer
		// session are replaced.
		Env:  []string{"PATH=/usr/bin:/bin", "PILOTHOUSE_SESSION_ID=s_outer", "PILOTHOUSE_TASK_ID=t_outer"},
		Cols: 91, Rows: 27,
	})
	s := attachSession(t, h.sock, true)
	s.waitOutput("env:1:" + h.sid + ":t_abc:xterm-256color")
	// Attaching resizes to the client's size (100x30); the initial size
	// (27 91) may already have been printed.
	if !strings.Contains(s.output(), "27 91") && !strings.Contains(s.output(), "30 100") {
		t.Fatalf("no size in %q", s.output())
	}
	s.send("\r")
	h.wait(t)
}

func TestHolderReportsToDaemon(t *testing.T) {
	testEnv(t)
	fd := startFakeDaemon(t)
	h := startHolder(t, HolderConfig{Argv: []string{"/bin/sh", "-c", fakeAgent}, Name: "rep", TaskID: "t_x"})
	s := attachSession(t, h.sock, true)
	s.waitOutput("ready")
	s.send("hi\r")
	if code := h.wait(t); code != 3 {
		t.Fatalf("code %d", code)
	}
	reqs := fd.requests()
	var paths []string
	for _, r := range reqs {
		paths = append(paths, r.Method+" "+r.Path)
	}
	if len(reqs) == 0 || reqs[0].Path != "/v1/sessions/wrapped" {
		t.Fatalf("first request should register: %v", paths)
	}
	var reg api.WrappedSessionBody
	if err := json.Unmarshal(reqs[0].Body, &reg); err != nil {
		t.Fatal(err)
	}
	if reg.SessionID != h.sid || reg.Title != "rep" || reg.TaskID != "t_x" || reg.PID == 0 ||
		reg.HolderPID != os.Getpid() || reg.HolderSock != h.sock || reg.Agent != api.AgentGeneric || len(reg.Cmdline) != 3 {
		t.Fatalf("registration: %+v", reg)
	}
	var sawIdle bool
	var exitBody *api.ExitBody
	for _, r := range reqs {
		switch r.Path {
		case "/v1/sessions/" + h.sid + "/osc":
			var b api.OSCBody
			_ = json.Unmarshal(r.Body, &b)
			sawIdle = sawIdle || b.State == "idle"
		case "/v1/sessions/" + h.sid + "/exit":
			exitBody = new(api.ExitBody)
			_ = json.Unmarshal(r.Body, exitBody)
		}
	}
	// Reports are coalesced: the final state must arrive; "working" may
	// have been superseded before it was sent.
	if !sawIdle {
		t.Fatalf("idle OSC not reported: %v", paths)
	}
	if exitBody == nil || exitBody.ExitCode != 3 {
		t.Fatalf("exit not reported: %v", paths)
	}
	if last := reqs[len(reqs)-1]; last.Path != "/v1/sessions/"+h.sid+"/exit" {
		t.Fatalf("exit should be the last report: %v", paths)
	}
}

// A daemon that starts after the holder still gets the registration and
// the latest state (retry every 2 s).
func TestHolderLateDaemon(t *testing.T) {
	testEnv(t)
	h := startHolder(t, HolderConfig{
		Argv:   []string{"/bin/sh", "-c", `printf '\033]7501;state=blocked;kind=question\007'; read x; exit 0`},
		Linger: 10 * time.Second,
	})
	time.Sleep(300 * time.Millisecond) // the first registration attempt fails
	fd := startFakeDaemon(t)
	waitFor(t, 6*time.Second, "registration and OSC", func() bool {
		var reg, osc bool
		for _, r := range fd.requests() {
			reg = reg || r.Path == "/v1/sessions/wrapped"
			osc = osc || (r.Path == "/v1/sessions/"+h.sid+"/osc" && strings.Contains(string(r.Body), `"kind":"question"`))
		}
		return reg && osc
	})
	s := attachSession(t, h.sock, false)
	s.send("\r")
	s.waitFrame(FrameExit)
	waitFor(t, 5*time.Second, "exit report", func() bool {
		for _, r := range fd.requests() {
			if r.Path == "/v1/sessions/"+h.sid+"/exit" {
				return true
			}
		}
		return false
	})
	// The holder would linger 10 s for late attaches; stop it now.
	h.stop()
	h.wait(t)
}

func TestHolderDetachReattachAndTakeover(t *testing.T) {
	testEnv(t)
	h := startHolder(t, HolderConfig{
		Argv: []string{"/bin/sh", "-c", `echo start; while read l; do echo "echo:$l"; [ "$l" = quit ] && exit 7; done`},
	})
	a := attachSession(t, h.sock, true)
	a.waitOutput("start")
	a.send("one\r")
	a.waitOutput("echo:one")

	// Clean detach: the holder keeps running.
	if err := a.c.Send(FrameDetach, nil); err != nil {
		t.Fatal(err)
	}
	<-a.done
	if info, err := Probe(context.Background(), h.sock); err != nil || info.Exited {
		t.Fatalf("holder gone after detach: %+v %v", info, err)
	}

	// Abrupt disconnect: still running.
	b := attachSession(t, h.sock, false)
	b.send("two\r")
	b.waitOutput("echo:two")
	b.c.Close()
	<-b.done

	// Reattach with replay sees earlier output.
	c := attachSession(t, h.sock, true)
	c.waitOutput("echo:one")
	c.waitOutput("echo:two")

	// A second attach takes over and the first is told why.
	d := attachSession(t, h.sock, false)
	f := c.waitFrame(FrameDetach)
	if msg, _ := DecodeText(f.Payload); !strings.Contains(msg, "another terminal") {
		t.Fatalf("takeover reason %q", msg)
	}
	<-c.done
	d.send("quit\r")
	d.waitOutput("echo:quit")
	if code, _ := DecodeExit(d.waitFrame(FrameExit).Payload); code != 7 {
		t.Fatalf("exit %d", code)
	}

	// During the linger a late attach learns the exit code.
	e := attachSession(t, h.sock, true)
	if !e.c.Hello.Exited || e.c.Hello.ExitCode != 7 {
		t.Fatalf("late hello %+v", e.c.Hello)
	}
	e.waitFrame(FrameExit)
	if code := h.wait(t); code != 7 {
		t.Fatalf("holder code %d", code)
	}
}

func TestHolderStopForwardsTERM(t *testing.T) {
	testEnv(t)
	h := startHolder(t, HolderConfig{
		Argv: []string{"/bin/sh", "-c", `trap 'echo got-term; exit 42' TERM; echo up; while :; do sleep 0.05; done`},
	})
	s := attachSession(t, h.sock, true)
	s.waitOutput("up")
	h.stop()
	if code := h.wait(t); code != 42 {
		t.Fatalf("code %d, want 42 (trap ran)", code)
	}
}

func TestHolderRefusesLiveSocketAndReplacesStale(t *testing.T) {
	testEnv(t)
	h := startHolder(t, HolderConfig{Argv: []string{"/bin/sh", "-c", "read x"}})
	_, err := RunHolder(context.Background(), HolderConfig{SessionID: h.sid, Argv: []string{"/bin/true"}})
	if err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Fatalf("second holder on a live socket: %v", err)
	}

	// A stale socket file (crashed holder) is replaced.
	sid := NewSessionID()
	if err := os.WriteFile(paths.HolderSocket(sid), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h2 := startHolder(t, HolderConfig{SessionID: sid, Argv: []string{"/bin/sh", "-c", "exit 0"}})
	h2.wait(t)
}

func TestHolderRejectsBadClients(t *testing.T) {
	testEnv(t)
	h := startHolder(t, HolderConfig{Argv: []string{"/bin/sh", "-c", "echo alive; read x"}})
	a := attachSession(t, h.sock, true)
	a.waitOutput("alive")

	// Wrong protocol version: refused with an error frame, and the
	// attached client is not disturbed.
	ctx := context.Background()
	c, err := Dial(ctx, h.sock, Hello{Mode: ModeAttach})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	raw, err := net.Dial("unix", h.sock)
	if err != nil {
		t.Fatal(err)
	}
	_ = WriteFrame(raw, FrameHello, []byte(`{"v":99,"mode":"attach"}`))
	f, err := ReadFrame(raw)
	if err != nil || f.Type != FrameError || !strings.Contains(string(f.Payload), "protocol version") {
		t.Fatalf("bad version: %v %v %q", err, f.Type, f.Payload)
	}
	raw.Close()
	// A peer that never says hello is dropped without effect; a probe
	// does not detach anyone.
	raw2, _ := net.Dial("unix", h.sock)
	defer raw2.Close()
	if _, err := Probe(ctx, h.sock); err != nil {
		t.Fatal(err)
	}
	// The Dial above (a real attach) took over from a, so reattach.
	b := attachSession(t, h.sock, false)
	b.send("\r")
	b.waitFrame(FrameExit)
	h.wait(t)
}
