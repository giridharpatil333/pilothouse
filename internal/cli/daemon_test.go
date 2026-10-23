package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRotatingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "daemon.log")
	rf, err := openRotatingFile(path, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	line := bytes.Repeat([]byte("x"), 39)
	line = append(line, '\n') // 40 bytes: two lines fit in 100, a third rotates
	for i := 0; i < 20; i++ {
		if _, err := rf.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := rf.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := rf.Write(line); err == nil {
		t.Fatal("write after close succeeded")
	}
	for _, name := range []string{"daemon.log", "daemon.log.1", "daemon.log.2", "daemon.log.3"} {
		fi, err := os.Stat(filepath.Join(filepath.Dir(path), name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if fi.Size() > 100 || fi.Size() == 0 {
			t.Errorf("%s has %d bytes", name, fi.Size())
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", name, fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(path + ".4"); !os.IsNotExist(err) {
		t.Fatalf("kept more than 3 old logs: %v", err)
	}

	// Reopening appends to the existing file and keeps counting its size.
	rf, err = openRotatingFile(path, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	if rf.size == 0 {
		t.Fatal("size of existing log not picked up")
	}
}
