// Package proc inspects processes: ancestry for the hook shim and the
// self-approval check, liveness for layer L0, Unix-socket peer PIDs.
// STUB: workstream B replaces the bodies.
package proc

import (
	"errors"
	"net"

	"github.com/xasdta/askgate/internal/adapters"
)

// Ancestors returns pid's ancestors, nearest first (pid itself excluded),
// up to max entries, stopping at PID 1.
func Ancestors(pid, max int) ([]adapters.Proc, error) { return nil, errors.New("not implemented") }

// Info returns one process.
func Info(pid int) (adapters.Proc, error) { return adapters.Proc{}, errors.New("not implemented") }

// Alive reports whether pid exists (signal 0).
func Alive(pid int) bool { return false }

// PeerPID returns the PID of the process on the other end of a Unix socket.
func PeerPID(c net.Conn) (int, error) { return 0, errors.New("not implemented") }
