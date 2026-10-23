package daemon

import (
	"fmt"
	"net"
	"path/filepath"

	"github.com/xasdta/pilothouse/internal/adapters"
	"github.com/xasdta/pilothouse/internal/proc"
)

// PeerGuard vets the process on the other end of a Unix-socket connection
// before an admin action (decide, presence change, policy reload, pairing,
// device revoke, shutdown). A non-nil error rejects the call with 403
// agent_caller and is shown to the caller.
type PeerGuard func(c net.Conn) error

// ProcessGuard is the default PeerGuard (BUILD-SPEC invariant 5): it
// rejects callers whose process, or any ancestor of it, is a coding agent,
// so an agent cannot approve its own requests by running `pilothouse approve`.
// It fails closed: a caller whose ancestry cannot be inspected is rejected.
func ProcessGuard(ads []adapters.Adapter) PeerGuard {
	names := map[string]bool{}
	for _, a := range ads {
		for _, n := range a.ProcessNames() {
			names[n] = true
		}
	}
	return processGuard(names, proc.PeerPID, proc.Info, proc.Ancestors)
}

func processGuard(
	agentNames map[string]bool,
	peerPID func(net.Conn) (int, error),
	info func(int) (adapters.Proc, error),
	ancestors func(pid, max int) ([]adapters.Proc, error),
) PeerGuard {
	return func(c net.Conn) error {
		pid, err := peerPID(c)
		if err != nil {
			return fmt.Errorf("cannot identify the calling process (%v); run this from your own terminal", err)
		}
		self, err := info(pid)
		if err != nil {
			return fmt.Errorf("cannot inspect the calling process %d (%v)", pid, err)
		}
		chain, err := ancestors(pid, 64)
		if err != nil {
			return fmt.Errorf("cannot inspect the ancestry of process %d (%v)", pid, err)
		}
		for _, p := range append([]adapters.Proc{self}, chain...) {
			if agentNames[filepath.Base(p.Comm)] {
				return fmt.Errorf("refused: this was run from inside an agent session (%s, pid %d); "+
					"agents cannot approve their own requests — run it from your own terminal", p.Comm, p.PID)
			}
		}
		return nil
	}
}
