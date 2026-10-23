package wrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/term"
)

// DetachKey is ctrl+\. Pressed once it detaches; pressed twice quickly it
// sends one literal ctrl+\ to the agent.
const DetachKey = 0x1c

// detachWait is how long the attach client waits for a second ctrl+\
// before treating the first one as "detach".
const detachWait = 400 * time.Millisecond

// redrawDelay separates the two resizes of the attach redraw nudge; many
// TUIs ignore a SIGWINCH whose size they already rendered, so the size
// must visibly change and then change back.
const redrawDelay = 60 * time.Millisecond

// AttachOptions configures an interactive attach.
type AttachOptions struct {
	Socket string
	Replay bool      // ask for the scrollback before live output
	Stdin  *os.File  // default os.Stdin
	Stdout *os.File  // default os.Stdout
	Stderr io.Writer // default os.Stderr; detach message goes here
}

// AttachResult says how an attach ended.
type AttachResult struct {
	SessionID string
	Name      string
	Exited    bool // the agent exited; ExitCode is valid
	ExitCode  int
	Detached  bool   // the user detached, or another terminal took over
	Reason    string // why the attach ended, for messages
}

type inChunk struct {
	b   []byte
	err error
}

type frameOrErr struct {
	f   Frame
	err error
}

// Attach connects this terminal to a holder until the user detaches, the
// agent exits, or ctx ends. The terminal is put in raw mode and always
// restored, including on SIGTERM/SIGHUP/SIGINT.
func Attach(ctx context.Context, o AttachOptions) (AttachResult, error) {
	if o.Stdin == nil {
		o.Stdin = os.Stdin
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	inFd := int(o.Stdin.Fd())
	isTerm := term.IsTerminal(inFd)
	cols, rows := termSize(o.Stdout, o.Stdin)

	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	conn, err := Dial(dctx, o.Socket, Hello{Mode: ModeAttach, Replay: o.Replay, Cols: cols, Rows: rows})
	cancel()
	if err != nil {
		return AttachResult{}, err
	}
	defer conn.Close()
	res := AttachResult{SessionID: conn.Hello.SessionID, Name: conn.Hello.Name}

	// Signals first, raw mode second: a signal arriving in between must
	// still find a handler that restores the terminal.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGWINCH, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT)
	defer signal.Stop(sigs)
	if isTerm {
		old, err := term.MakeRaw(inFd)
		if err != nil {
			return res, fmt.Errorf("wrap: raw mode: %w", err)
		}
		defer func() { _ = term.Restore(inFd, old) }()
	}

	modes := newModeTracker()
	out := o.Stdout

	done := make(chan struct{})
	defer close(done)
	frames := make(chan frameOrErr, 16)
	go func() {
		for {
			f, err := conn.Recv()
			select {
			case frames <- frameOrErr{f, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	// The stdin reader cannot be interrupted portably (a read on a TTY
	// has no deadline); it ends with the process, which `pilothouse attach`
	// does right after Attach returns.
	input := make(chan inChunk, 16)
	go func() {
		for {
			buf := make([]byte, dataChunk)
			n, err := o.Stdin.Read(buf)
			select {
			case input <- inChunk{buf[:n], err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	// Redraw nudge: shrink by one row, then restore.
	var nudge <-chan time.Time
	if !conn.Hello.Exited && rows > 1 {
		if conn.Send(FrameResize, EncodeResize(cols, rows-1)) == nil {
			nudge = time.After(redrawDelay)
		}
	}

	var (
		armed    bool // first ctrl+\ seen
		armTimer *time.Timer
		armC     <-chan time.Time
	)
	stopArm := func() {
		armed, armC = false, nil
		if armTimer != nil {
			armTimer.Stop()
		}
	}
	defer stopArm()

	finish := func(r AttachResult) (AttachResult, error) {
		// Leave the user's terminal as we found it: undo the modes the
		// agent's TUI switched on (alternate screen, mouse, …).
		if u := modes.undo(); len(u) > 0 {
			_, _ = out.Write(u)
		}
		return r, nil
	}
	detach := func(reason string) (AttachResult, error) {
		_ = conn.Send(FrameDetach, nil)
		res.Detached, res.Reason = true, reason
		return finish(res)
	}

	for {
		select {
		case <-ctx.Done():
			return detach("cancelled")
		case s := <-sigs:
			if s == syscall.SIGWINCH {
				cols, rows = termSize(o.Stdout, o.Stdin)
				_ = conn.Send(FrameResize, EncodeResize(cols, rows))
				continue
			}
			return detach("signal " + s.String())
		case <-nudge:
			nudge = nil
			_ = conn.Send(FrameResize, EncodeResize(cols, rows))
		case <-armC:
			stopArm()
			return detach("detach key")
		case in := <-input:
			if len(in.b) > 0 {
				send, act := scanDetach(in.b, &armed)
				if len(send) > 0 {
					if err := conn.Send(FrameData, send); err != nil {
						res.Reason = "connection to holder lost"
						return finish(res)
					}
				}
				switch act {
				case actDetach:
					stopArm()
					return detach("detach key")
				case actArm:
					if armTimer == nil {
						armTimer = time.NewTimer(detachWait)
					} else {
						armTimer.Reset(detachWait)
					}
					armC = armTimer.C
				case actDisarm:
					stopArm()
				}
			}
			if in.err != nil {
				// stdin closed (piped input ran out): stay attached for
				// output, the way `ssh` does with -n.
				input = nil
			}
		case fe := <-frames:
			if fe.err != nil {
				res.Reason = "connection to holder lost"
				if errors.Is(fe.err, io.EOF) {
					res.Reason = "holder closed the connection"
				}
				return finish(res)
			}
			switch fe.f.Type {
			case FrameData:
				modes.Feed(fe.f.Payload)
				if _, err := out.Write(fe.f.Payload); err != nil {
					return detach("output closed")
				}
			case FrameExit:
				code, err := DecodeExit(fe.f.Payload)
				if err != nil {
					return res, err
				}
				res.Exited, res.ExitCode = true, code
				return finish(res)
			case FrameDetach:
				reason, _ := DecodeText(fe.f.Payload)
				res.Detached, res.Reason = true, reason
				return finish(res)
			case FrameError:
				msg, _ := DecodeText(fe.f.Payload)
				res.Reason = msg
				return finish(res)
			}
		}
	}
}

type detachAction int

const (
	actNone   detachAction = iota
	actArm                 // first ctrl+\: wait for a second one
	actDisarm              // second ctrl+\: one literal was emitted
	actDetach              // detach now
)

// scanDetach applies the ctrl+\ rules to one chunk of input. It returns
// the bytes to forward and what to do next. armed carries "one ctrl+\
// pending" across chunks. Any other byte after a pending ctrl+\ means the
// user meant to detach; the rest of the chunk is dropped.
func scanDetach(in []byte, armed *bool) ([]byte, detachAction) {
	act := actNone
	out := make([]byte, 0, len(in))
	for _, c := range in {
		if *armed {
			*armed = false
			if c == DetachKey {
				out = append(out, DetachKey)
				act = actDisarm
				continue
			}
			return out, actDetach
		}
		if c == DetachKey {
			*armed = true
			act = actArm
			continue
		}
		out = append(out, c)
	}
	return out, act
}

// termSize returns the window size, trying stdout then stdin, else 80x24.
func termSize(files ...*os.File) (uint16, uint16) {
	for _, f := range files {
		if c, r, err := term.GetSize(int(f.Fd())); err == nil && c > 0 && r > 0 {
			return uint16(min(c, 0xffff)), uint16(min(r, 0xffff))
		}
	}
	return 80, 24
}

// DetachMessage is the one line printed after a detach.
func DetachMessage(r AttachResult) string {
	what := "detached from"
	if r.Reason != "" && r.Reason != "detach key" && !isSignalReason(r.Reason) {
		what = "detached (" + r.Reason + ") from"
	}
	return fmt.Sprintf("%s %s (%s) — reattach: pilothouse attach %s", what, r.Name, r.SessionID, r.SessionID)
}

func isSignalReason(s string) bool { return len(s) > 7 && s[:7] == "signal " }
