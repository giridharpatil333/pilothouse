//go:build darwin

package proc

import (
	"encoding/binary"
	"reflect"
	"testing"
)

func TestParseProcArgs2(t *testing.T) {
	buf := binary.NativeEndian.AppendUint32(nil, 3)
	buf = append(buf, "/usr/local/bin/codex\x00\x00\x00\x00codex\x00resume\x00abc\x00HOME=/x\x00"...)
	got, err := parseProcArgs2(buf)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"codex", "resume", "abc"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if _, err := parseProcArgs2([]byte{1}); err == nil {
		t.Error("short buffer accepted")
	}
}
