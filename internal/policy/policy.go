// Package policy evaluates user, repo and built-in rules at the pre_tool
// checkpoint. STUB: workstream C replaces the bodies; signatures are fixed.
package policy

import (
	"encoding/json"
	"time"

	"github.com/xasdta/askgate/internal/api"
)

// Input describes one tool call.
type Input struct {
	Agent      api.AgentKind   `json:"agent"`
	Tool       string          `json:"tool"`
	ToolKind   api.ToolKind    `json:"kind"`
	Command    string          `json:"command,omitempty"`
	Paths      []string        `json:"paths,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Cwd        string          `json:"cwd"`
	Repo       string          `json:"repo,omitempty"`
	Branch     string          `json:"branch,omitempty"`
	Mode       api.Mode        `json:"mode,omitempty"`
	Checkpoint api.Checkpoint  `json:"checkpoint"`
	Now        time.Time       `json:"-"` // zero = time.Now()
}

// Result is the combined verdict. Action "" means no rule matched.
// Precedence: deny > ask > allow.
type Result struct {
	Action  api.Behavior `json:"action,omitempty"`
	Rule    string       `json:"rule,omitempty"`    // name of the deciding rule
	File    string       `json:"file,omitempty"`    // file it came from ("builtin" for built-ins)
	Message string       `json:"message,omitempty"` // shown to the agent on deny/ask
	Matched []Match      `json:"matched,omitempty"` // every rule that matched
}

type Match struct {
	Rule    string       `json:"rule"`
	File    string       `json:"file"`
	Action  api.Behavior `json:"action"`
	Ignored string       `json:"ignored,omitempty"` // e.g. "allow rule in untrusted repo"
}

type Options struct {
	UserFile         string   // paths.Policy()
	TrustedReposFile string   // paths.TrustedRepos()
	ProtectedPaths   []string // from every adapter + paths.Home()
}

// Engine is safe for concurrent use.
type Engine struct{ opts Options }

// Load builds an engine. It never returns nil: on errors the engine keeps
// the built-in rules and reports problems in the status.
func Load(opts Options) (*Engine, api.PolicyStatus) {
	return &Engine{opts: opts}, api.PolicyStatus{}
}

func (e *Engine) Evaluate(in Input) Result { return Result{} }
func (e *Engine) Status() api.PolicyStatus { return api.PolicyStatus{} }

// Changed reports whether any loaded user file changed on disk since Load.
func (e *Engine) Changed() bool { return false }
