package daemon

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// gitCache remembers each directory's repo root and branch for ttl, so the
// pre_tool hot path runs git at most once per directory per ttl.
type gitCache struct {
	mu   sync.Mutex
	ttl  time.Duration
	now  func() time.Time
	run  func(ctx context.Context, dir string) (root, branch string)
	m    map[string]gitEntry
	runs int // lookups that ran git (tests)
}

type gitEntry struct {
	root, branch string
	at           time.Time
}

func newGitCache(ttl time.Duration, now func() time.Time, run func(context.Context, string) (string, string)) *gitCache {
	return &gitCache{ttl: ttl, now: now, run: run, m: map[string]gitEntry{}}
}

// lookup returns dir's repo root and branch ("" outside a repository).
func (g *gitCache) lookup(ctx context.Context, dir string) (root, branch string) {
	if dir == "" {
		return "", ""
	}
	now := g.now()
	g.mu.Lock()
	e, ok := g.m[dir]
	g.mu.Unlock()
	if ok && now.Sub(e.at) < g.ttl {
		return e.root, e.branch
	}
	root, branch = g.run(ctx, dir)
	g.mu.Lock()
	if len(g.m) > 4096 { // bound memory: directories are many, sessions few
		clear(g.m)
	}
	g.m[dir] = gitEntry{root: root, branch: branch, at: now}
	g.runs++
	g.mu.Unlock()
	return root, branch
}

// gitRepoInfo asks git for dir's top level and current branch.
func gitRepoInfo(ctx context.Context, dir string) (root, branch string) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel", "--abbrev-ref", "HEAD")
	// Don't take index.lock: we run next to agents that are using git.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return "", ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return strings.TrimSpace(lines[0]), ""
	}
	return strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
}
