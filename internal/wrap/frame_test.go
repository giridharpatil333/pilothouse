package wrap

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	frames := []Frame{
		{FrameHello, encodeHello(Hello{V: 1, Mode: ModeAttach, Cols: 80, Rows: 24})},
		{FrameData, []byte("hello\x1b[0m")},
		{FrameData, nil},
		{FrameResize, EncodeResize(132, 43)},
		{FrameDetach, nil},
		{FrameExit, EncodeExit(-3)},
		{FrameError, []byte("boom")},
	}
	var buf bytes.Buffer
	for _, f := range frames {
		if err := WriteFrame(&buf, f.Type, f.Payload); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range frames {
		got, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if got.Type != want.Type || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("frame %d: got %v %q, want %v %q", i, got.Type, got.Payload, want.Type, want.Payload)
		}
	}
	if _, err := ReadFrame(&buf); err != io.EOF {
		t.Fatalf("at end: %v, want EOF", err)
	}
}

func TestFrameLimits(t *testing.T) {
	if err := WriteFrame(io.Discard, FrameData, make([]byte, MaxFrame+1)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversize write: %v", err)
	}
	if err := WriteFrame(io.Discard, FrameData, make([]byte, MaxFrame)); err != nil {
		t.Fatalf("max-size write: %v", err)
	}
	// A header announcing 4 GB must be refused before anything is allocated.
	h := []byte{byte(FrameData), 0, 0, 0, 0}
	binary.BigEndian.PutUint32(h[1:], 0xffffffff)
	if _, err := ReadFrame(bytes.NewReader(h)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversize header: %v", err)
	}
	// Truncated payload.
	binary.BigEndian.PutUint32(h[1:], 10)
	if _, err := ReadFrame(bytes.NewReader(append(h, 'a', 'b'))); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated payload: %v", err)
	}
	// Truncated header.
	if _, err := ReadFrame(bytes.NewReader(h[:3])); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated header: %v", err)
	}
}

func TestPayloadCodecs(t *testing.T) {
	c, r, err := DecodeResize(EncodeResize(200, 50))
	if err != nil || c != 200 || r != 50 {
		t.Fatalf("resize: %d %d %v", c, r, err)
	}
	for _, p := range [][]byte{nil, {1, 2, 3}, EncodeResize(0, 10), EncodeResize(10, 0)} {
		if _, _, err := DecodeResize(p); err == nil {
			t.Fatalf("resize %v accepted", p)
		}
	}
	for _, code := range []int{0, 3, 255, -1, 130} {
		got, err := DecodeExit(EncodeExit(code))
		if err != nil || got != code {
			t.Fatalf("exit %d: %d %v", code, got, err)
		}
	}
	if _, err := DecodeExit([]byte{1}); err == nil {
		t.Fatal("short exit accepted")
	}
	if _, err := DecodeText([]byte{0xff, 0xfe}); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	if _, err := DecodeText(make([]byte, maxText+1)); err == nil {
		t.Fatal("oversized text accepted")
	}
	if got := encodeText(strings.Repeat("é", maxText)); len(got) > maxText || !bytes.Equal(got, []byte(string(got))) {
		t.Fatalf("encodeText: %d bytes", len(got))
	}
}

func TestHelloVersioning(t *testing.T) {
	if _, err := DecodeHello(encodeHello(Hello{V: ProtocolVersion, Mode: ModeProbe})); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeHello(encodeHello(Hello{V: ProtocolVersion + 1, Mode: ModeAttach})); err == nil || !strings.Contains(err.Error(), "protocol version") {
		t.Fatalf("future version: %v", err)
	}
	if _, err := DecodeHello(encodeHello(Hello{V: ProtocolVersion, Mode: "steal"})); err == nil {
		t.Fatal("unknown mode accepted")
	}
	if _, err := DecodeHello([]byte("{")); err == nil {
		t.Fatal("bad JSON accepted")
	}
	if _, err := DecodeHello(bytes.Repeat([]byte(" "), maxHello+1)); err == nil {
		t.Fatal("oversized hello accepted")
	}
	if _, err := DecodeServerHello(encodeHello(ServerHello{V: 0})); err == nil {
		t.Fatal("server hello without version accepted")
	}
}

// FuzzReadFrame feeds arbitrary bytes to the decoder: it must never panic
// or return a payload over the limit, and every frame it accepts must
// re-encode to exactly the bytes it was read from.
func FuzzReadFrame(f *testing.F) {
	seed := func(t FrameType, p []byte) []byte {
		b, _ := AppendFrame(nil, t, p)
		return b
	}
	f.Add(seed(FrameHello, encodeHello(Hello{V: 1, Mode: ModeAttach, Replay: true, Cols: 80, Rows: 24})))
	f.Add(seed(FrameData, []byte("ls -la\r")))
	f.Add(append(seed(FrameResize, EncodeResize(80, 24)), seed(FrameExit, EncodeExit(3))...))
	f.Add(seed(FrameDetach, nil))
	f.Add([]byte{byte(FrameData), 0xff, 0xff, 0xff, 0xff})
	f.Add([]byte{byte(FrameError), 0, 0, 0, 9, 'x'})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		r := bytes.NewReader(b)
		consumed := 0
		for {
			fr, err := ReadFrame(r)
			if err != nil {
				return
			}
			if len(fr.Payload) > MaxFrame {
				t.Fatalf("payload %d over limit", len(fr.Payload))
			}
			enc, err := AppendFrame(nil, fr.Type, fr.Payload)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(enc, b[consumed:consumed+len(enc)]) {
				t.Fatalf("re-encoding differs at offset %d", consumed)
			}
			consumed += len(enc)
			// Payload decoders must reject or accept, never panic.
			switch fr.Type {
			case FrameHello:
				_, _ = DecodeHello(fr.Payload)
				_, _ = DecodeServerHello(fr.Payload)
			case FrameResize:
				_, _, _ = DecodeResize(fr.Payload)
			case FrameExit:
				_, _ = DecodeExit(fr.Payload)
			case FrameError, FrameDetach:
				_, _ = DecodeText(fr.Payload)
			}
		}
	})
}
