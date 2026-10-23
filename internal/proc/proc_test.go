package proc

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInfoSelf(t *testing.T) {
	p, err := Info(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if p.PID != os.Getpid() || p.PPID != os.Getppid() {
		t.Fatalf("Info(self) = %+v, want pid %d ppid %d", p, os.Getpid(), os.Getppid())
	}
	if want := filepath.Base(os.Args[0]); p.Comm != want {
		t.Errorf("Comm = %q, want %q", p.Comm, want)
	}
	if !strings.Contains(p.Args, "-test.") {
		t.Errorf("Args = %q, want the test binary's flags", p.Args)
	}
}

func TestInfoMissing(t *testing.T) {
	if _, err := Info(0); err == nil {
		t.Error("Info(0) succeeded")
	}
	if _, err := Info(1 << 30); err == nil {
		t.Error("Info(huge pid) succeeded")
	}
}

// `go test` runs the test binary as a child of the go command, so the
// ancestry must include it.
func TestAncestorsIncludesGo(t *testing.T) {
	anc, err := Ancestors(os.Getpid(), 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(anc) == 0 || anc[0].PID != os.Getppid() {
		t.Fatalf("Ancestors()[0] = %+v, want parent %d", anc, os.Getppid())
	}
	found := false
	for i, p := range anc {
		if p.PID == 1 {
			t.Errorf("ancestors include PID 1: %+v", anc)
		}
		if i > 0 && anc[i-1].PPID != p.PID {
			t.Errorf("chain broken at %d: %+v", i, anc)
		}
		if p.Comm == "go" {
			found = true
		}
	}
	if !found && os.Getenv("ASKGATE_TEST_NO_GO_PARENT") == "" {
		t.Errorf("no `go` in ancestry: %+v", anc)
	}
	if got, _ := Ancestors(os.Getpid(), 1); len(got) != 1 {
		t.Errorf("max=1 returned %d entries", len(got))
	}
	if got, _ := Ancestors(os.Getpid(), 0); got != nil {
		t.Errorf("max=0 returned %+v", got)
	}
}

func TestAlive(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Error("Alive(self) = false")
	}
	if Alive(0) || Alive(-1) {
		t.Error("Alive(<=0) = true")
	}
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	// The child has been reaped; its PID is (almost certainly) free.
	if Alive(cmd.Process.Pid) {
		t.Skip("PID reused already")
	}
}

func TestPeerPIDUnixSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "agp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()
	client, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, ok := <-accepted
	if !ok {
		t.Fatal("accept failed")
	}
	defer server.Close()
	for name, c := range map[string]net.Conn{"server side": server, "client side": client} {
		pid, err := PeerPID(c)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if pid != os.Getpid() {
			t.Errorf("%s: PeerPID = %d, want %d", name, pid, os.Getpid())
		}
	}
}

func TestPeerPIDNotSocket(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if _, err := PeerPID(a); err == nil {
		t.Error("PeerPID(net.Pipe) succeeded")
	}
}

func TestCommFromArgv(t *testing.T) {
	for _, tc := range []struct{ argv0, kernel, want string }{
		{"/opt/homebrew/bin/claude", "claude.exe", "claude"},
		{"-zsh", "zsh", "zsh"},
		{"", "codex", "codex"},
		{"/", "launchd", "launchd"},
		{"node", "node", "node"},
	} {
		if got := commFromArgv(tc.argv0, tc.kernel); got != tc.want {
			t.Errorf("commFromArgv(%q, %q) = %q, want %q", tc.argv0, tc.kernel, got, tc.want)
		}
	}
}

func TestJoinArgsCaps(t *testing.T) {
	long := strings.Repeat("é", maxArgs) // 2 bytes per rune
	got := joinArgs([]string{"x", long})
	if len(got) > maxArgs {
		t.Fatalf("len = %d", len(got))
	}
	if !strings.HasPrefix(got, "x é") || strings.ContainsRune(got, '�') {
		t.Errorf("bad truncation: %q", got[len(got)-8:])
	}
}
