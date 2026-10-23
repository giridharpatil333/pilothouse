//go:build linux

package proc

import "testing"

func TestParseStat(t *testing.T) {
	comm, ppid, err := parseStat([]byte("4242 (my (odd) name) S 17 4242 4242 0 -1"))
	if err != nil {
		t.Fatal(err)
	}
	if comm != "my (odd) name" || ppid != 17 {
		t.Errorf("got %q %d", comm, ppid)
	}
	if _, _, err := parseStat([]byte("garbage")); err == nil {
		t.Error("garbage accepted")
	}
}
